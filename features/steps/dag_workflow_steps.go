package steps

import (
	"context"
	"errors"
	"flow-shift/shared"
	"flow-shift/workflow"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite" // For potential mock activity needs if not using real activities
	"go.uber.org/zap"
)

// TaskQueueName is the task queue used for ITs. Must match worker.
const TaskQueueName = "dynamic-dag-task-queue" // Same as in main.go

// WorkflowTestContext holds state across steps in a scenario.
type WorkflowTestContext struct {
	temporalClient client.Client
	logger         *zap.Logger
	workflowID     string
	workflowRun    client.WorkflowRun
	workflowConfig shared.WorkflowConfig
	lastResult     map[string]interface{}
	lastError      error

	// For mocking activities if needed directly in ITs, though complex for e2e
	// mockActivities map[string]func(ctx context.Context, params ...interface{}) (interface{}, error)

	// Store specific per-activity mock behaviors
	// Key: "NodeID::ActivityName" or just "ActivityName" if global
	activityMocks map[string][]activityMockBehavior
}

type activityMockBehavior struct {
	attempt         int    // For which attempt this mock applies (0 or 1 for first, etc.)
	failCount       int    // How many times to fail before this behavior
	succeedWith     interface{} // Data to return on success
	failWithError   error       // Error to return for failure
	expectParams    map[string]interface{} // If set, params must contain these
	timesCalled     int    // Internal counter for this specific behavior
	maxCalls        int    // Max times this specific behavior should be called (usually 1)
	simulateTimeout bool   // If true, simulate a StartToClose timeout
}


// NewWorkflowTestContext creates a new context for a scenario.
func NewWorkflowTestContext() *WorkflowTestContext {
	// Setup logger
	config := zap.NewDevelopmentConfig()
	// config.Level = zap.NewAtomicLevelAt(zap.DebugLevel) // Or ErrorLevel for less noise
	logger, _ := config.Build()

	return &WorkflowTestContext{
		logger: logger,
		// mockActivities: make(map[string]func(ctx context.Context, params ...interface{}) (interface{}, error)),
		activityMocks: make(map[string][]activityMockBehavior),
	}
}

// RegisterSteps connects Gherkin steps to Go functions.
func (wtc *WorkflowTestContext) RegisterSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a Temporal dev server is running$`, wtc.aTemporalDevServerIsRunning)
	ctx.Step(`^I have a Temporal client connection$`, wtc.iHaveATemporalClientConnection)
	ctx.Step(`^the workflow configuration is loaded from "([^"]*)"$`, wtc.theWorkflowConfigurationIsLoadedFrom)
	ctx.Step(`^the workflow configuration is loaded from "([^"]*)" specifically for start nodes "([^"]*)"$`, wtc.theWorkflowConfigurationIsLoadedFromSpecificallyForStartNodes)
	ctx.Step(`^I override the activity "([^"]*)::([^"]*)" to return:$`, wtc.iOverrideTheActivityToReturn) // NodeID::ActivityName
	ctx.Step(`^I override the activity "([^"]*)::([^"]*)" to expect params containing:$`, wtc.iOverrideTheActivityToExpectParamsContaining)
	ctx.Step(`^I mock activity "([^"]*)::([^"]*)" attempt (\d+) to return "([^"]*)"$`, wtc.iMockActivityAttemptToReturn)
	ctx.Step(`^I override the activity "([^"]*)::([^"]*)" to fail (\d+) times then succeed with "([^"]*)"$`, wtc.iOverrideActivityToFailTimesThenSucceed)
	ctx.Step(`^I mock activity "([^"]*)" to simulate a StartToClose timeout$`, wtc.iMockActivityToSimulateAStartToCloseTimeout)
	ctx.Step(`^I mock activity "([^"]*)" to return "([^"]*)"$`, wtc.iMockActivityToReturn) // Global activity mock by name
	ctx.Step(`^a workflow configuration with a node "([^"]*)" having:$`, wtc.aWorkflowConfigurationWithANodeHaving)
	ctx.Step(`^node "([^"]*)" which depends on "([^"]*)" and runs "([^"]*)"$`, wtc.nodeWhichDependsOnAndRuns)
	ctx.Step(`^I start the "([^"]*)" with workflow ID "([^"]*)"$`, wtc.iStartTheWorkflowWithID)
	ctx.Step(`^I start the "DAGWorkflow" with this configuration and ID "([^"]*)"$`, wtc.iStartDAGWorkflowWithThisConfigurationAndID)

	ctx.Step(`^I send the signal "([^"]*)" with payload (?:json )?"([^"]*)" to workflow "([^"]*)"$`, wtc.iSendTheSignalWithPayloadToWorkflow)
	ctx.Step(`^I send the signal "([^"]*)" with payload:$`, wtc.iSendTheSignalWithPayloadDocStringToWorkflow)
	ctx.Step(`^I send the signal "([^"]*)" to workflow "([^"]*)"$`, wtc.iSendTheSignalToWorkflow)

	ctx.Step(`^the workflow "([^"]*)" should complete successfully$`, wtc.theWorkflowShouldCompleteSuccessfully)
	ctx.Step(`^the workflow "([^"]*)" should complete successfully within (\d+) seconds$`, wtc.theWorkflowShouldCompleteSuccessfullyWithinSeconds)
	ctx.Step(`^the workflow "([^"]*)" should complete with an error containing "([^"]*)"$`, wtc.theWorkflowShouldCompleteWithErrorContaining)

	ctx.Step(`^the result for node "([^"]*)" should not be nil$`, wtc.theResultForNodeShouldNotBeNil)
	ctx.Step(`^the result for node "([^"]*)" should be nil$`, wtc.theResultForNodeShouldBeNil)
	ctx.Step(`^the result for node "([^"]*)" should be "([^"]*)"$`, wtc.theResultForNodeShouldBe)
	ctx.Step(`^activity "([^"]*)::([^"]*)" should not have been called$`, wtc.activityShouldNotHaveBeenCalled)
	ctx.Step(`^I wait for (\d+) second(?:s)?$`, wtc.iWaitForSeconds)
	ctx.Step(`^I wait for workflow "([^"]*)" to process signal and advance time by (\d+) seconds$`, wtc.iWaitForWorkflowToProcessSignalAndAdvanceTimeBySeconds)


	// Before each scenario, create a new client and clear state
	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		wtc.workflowID = ""
		wtc.workflowRun = nil
		wtc.lastResult = nil
		wtc.lastError = nil
		wtc.workflowConfig = shared.WorkflowConfig{} // Reset config
		wtc.activityMocks = make(map[string][]activityMockBehavior) // Reset mocks

		// Potentially connect to Temporal here or ensure connection is alive
		// For simplicity, assuming client is created once or managed externally if needed
		if wtc.temporalClient == nil {
			c, err := client.Dial(client.Options{
				HostPort: client.DefaultHostPort,
				Logger:   wtc.logger,
			})
			if err != nil {
				return ctx, fmt.Errorf("unable to create Temporal client for scenario: %w", err)
			}
			wtc.temporalClient = c
		}
		return ctx, nil
	})

	ctx.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		// if wtc.temporalClient != nil {
		// 	wtc.temporalClient.Close() // Close client after each scenario if created per scenario
		// 	wtc.temporalClient = nil
		// }
		// For long-running test suites, creating client once might be better.
		// For now, let's assume client is longer-lived or managed by a suite initializer.
		return ctx, nil
	})
}

// --- Step Implementations (Placeholders and a few examples) ---

func (wtc *WorkflowTestContext) aTemporalDevServerIsRunning() error {
	// This step is mostly a prerequisite reminder.
	// Actual check could involve trying to connect or pinging the server.
	// For now, we assume it's up.
	wtc.logger.Info("Assuming Temporal dev server is running.")
	return nil
}

func (wtc *WorkflowTestContext) iHaveATemporalClientConnection() error {
	if wtc.temporalClient == nil {
		c, err := client.Dial(client.Options{
			HostPort: client.DefaultHostPort,
			Logger:   wtc.logger,
		})
		if err != nil {
			return fmt.Errorf("failed to create Temporal client: %w", err)
		}
		wtc.temporalClient = c
	}
	err := wtc.temporalClient.Connection().Ping(context.Background())
	if err != nil {
		return fmt.Errorf("failed to ping Temporal server: %w", err)
	}
	wtc.logger.Info("Successfully connected to Temporal server.")
	return nil
}

func (wtc *WorkflowTestContext) theWorkflowConfigurationIsLoadedFrom(configFile string) error {
	yamlFile, err := os.ReadFile(configFile) // Assumes configFile is relative to features dir or project root
	if err != nil {
		// Try relative to project root if not found in current (features)
		yamlFile, err = os.ReadFile("../" + configFile)
		if err != nil {
			return fmt.Errorf("failed to read config file %s: %w", configFile, err)
		}
	}
	// Also attempt to load rules.yaml
	rulesFile, errRules := os.ReadFile("../rules.yaml") // Assuming rules.yaml is at project root, steps are in features/steps
	if errRules != nil {
		wtc.logger.Warn("rules.yaml not found or could not be read, proceeding without external rules.", zap.Error(errRules))
		rulesFile = []byte{} // Empty bytes, LoadFullWorkflowConfigFromYAMLs will handle it
	}

	wtc.workflowConfig, err = workflow.LoadFullWorkflowConfigFromYAMLs(yamlFile, rulesFile)
	if err != nil {
		return fmt.Errorf("failed to load full workflow config (dag+rules) from %s: %w", configFile, err)
	}
	wtc.logger.Info("Workflow config and rules loaded", zap.String("dagFile", configFile))
	return nil
}

func (wtc *WorkflowTestContext) theWorkflowConfigurationIsLoadedFromSpecificallyForStartNodes(configFile, startNodesStr string) error {
	// This step will now also load rules associated with the configFile
	if err := wtc.theWorkflowConfigurationIsLoadedFrom(configFile); err != nil {
		return err
	}
	startNodes := strings.Split(startNodesStr, ",")
    trimmedStartNodes := make([]string, len(startNodes))
    for i, sn := range startNodes {
        trimmedStartNodes[i] = strings.TrimSpace(sn)
    }
    wtc.workflowConfig.StartNodeIDs = trimmedStartNodes
    wtc.logger.Info("Workflow config start nodes overridden", zap.Strings("startNodes", wtc.workflowConfig.StartNodeIDs))
    return nil
}


func (wtc *WorkflowTestContext) iStartTheWorkflowWithID(workflowName string, wfID string) error {
	if workflowName != "DAGWorkflow" { // Assuming our main workflow is DAGWorkflow
		return fmt.Errorf("this step currently only supports starting 'DAGWorkflow', got '%s'", workflowName)
	}
	wtc.workflowID = wfID
	if wtc.workflowConfig.Nodes == nil {
		return fmt.Errorf("workflow configuration not loaded before starting workflow")
	}

	workflowInput := workflow.DAGWorkflowInput{Config: wtc.workflowConfig}
	options := client.StartWorkflowOptions{
		ID:        wtc.workflowID,
		TaskQueue: TaskQueueName,
	}

	wtc.logger.Info("Starting workflow", zap.String("ID", wtc.workflowID), zap.String("TaskQueue", TaskQueueName))
	run, err := wtc.temporalClient.ExecuteWorkflow(context.Background(), options, workflow.DAGWorkflow, workflowInput)
	if err != nil {
		return fmt.Errorf("failed to start workflow %s: %w", wtc.workflowID, err)
	}
	wtc.workflowRun = run
	wtc.logger.Info("Workflow started", zap.String("RunID", wtc.workflowRun.GetRunID()))
	return nil
}

func (wtc *WorkflowTestContext) iStartDAGWorkflowWithThisConfigurationAndID(wfID string) error {
    // This step assumes wtc.workflowConfig is already populated by a prior step like "a workflow configuration with a node..."
    if wtc.workflowConfig.Nodes == nil || len(wtc.workflowConfig.StartNodeIDs) == 0 {
        return fmt.Errorf("workflow configuration not properly set up before this step")
    }
    return wtc.iStartTheWorkflowWithID("DAGWorkflow", wfID)
}


func (wtc *WorkflowTestContext) iSendTheSignalWithPayloadToWorkflow(signalName, payloadJSON, wfID string) error {
	if wtc.workflowRun == nil || (wtc.workflowID != wfID && wfID != "") { // wfID in step can be for verification
		// Try to get handle if not set (e.g. workflow started in a different process/test setup)
		// For now, assume wtc.workflowRun is set by a previous step in the same scenario.
		return fmt.Errorf("workflow run handle not available for ID '%s'. Was it started in this scenario?", wfID)
	}

	var payload interface{}
	// Attempt to unmarshal if it looks like a JSON object/array, otherwise treat as string
	if (strings.HasPrefix(payloadJSON, "{") && strings.HasSuffix(payloadJSON, "}")) ||
	   (strings.HasPrefix(payloadJSON, "[") && strings.HasSuffix(payloadJSON, "]")) {
		err := json.Unmarshal([]byte(payloadJSON), &payload)
		if err != nil {
			// Fallback to string if unmarshal fails
			wtc.logger.Debug("Signal payload JSON unmarshal failed, treating as string", zap.Error(err), zap.String("payload", payloadJSON))
			payload = payloadJSON
		}
	} else {
		payload = payloadJSON // Treat as plain string if not obviously JSON
	}


	wtc.logger.Info("Sending signal to workflow",
		zap.String("WorkflowID", wtc.workflowID),
		zap.String("SignalName", signalName),
		zap.Any("Payload", payload))

	err := wtc.temporalClient.SignalWorkflow(context.Background(), wtc.workflowID, "", signalName, payload)
	if err != nil {
		return fmt.Errorf("failed to send signal '%s' to workflow '%s': %w", signalName, wtc.workflowID, err)
	}
	return nil
}

func (wtc *WorkflowTestContext) iSendTheSignalWithPayloadDocStringToWorkflow(signalName string, payloadDocString *godog.DocString) error {
    // wfID is implicit from context
    if wtc.workflowRun == nil {
        return fmt.Errorf("workflow run handle not available. Was it started in this scenario?")
    }
    return wtc.iSendTheSignalWithPayloadToWorkflow(signalName, payloadDocString.Content, wtc.workflowID)
}


func (wtc *WorkflowTestContext) iSendTheSignalToWorkflow(signalName, wfID string) error {
	return wtc.iSendTheSignalWithPayloadToWorkflow(signalName, "{}", wfID) // Send empty JSON object as default payload
}


func (wtc *WorkflowTestContext) theWorkflowShouldCompleteSuccessfully() error {
	if wtc.workflowRun == nil {
		return fmt.Errorf("workflow run handle not available for ID '%s'", wtc.workflowID)
	}
	var result map[string]interface{}
	err := wtc.workflowRun.Get(context.Background(), &result) // Blocks until completion
	wtc.lastResult = result
	wtc.lastError = err
	if err != nil {
		return fmt.Errorf("workflow '%s' did not complete successfully: %w", wtc.workflowID, err)
	}
	wtc.logger.Info("Workflow completed successfully", zap.String("ID", wtc.workflowID), zap.Any("Result", result))
	return nil
}

func (wtc *WorkflowTestContext) theWorkflowShouldCompleteSuccessfullyWithinSeconds(timeoutSec int) error {
    if wtc.workflowRun == nil {
        return fmt.Errorf("workflow run handle not available for ID '%s'", wtc.workflowID)
    }

    ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
    defer cancel()

    var result map[string]interface{}
    err := wtc.workflowRun.Get(ctx, &result) // Blocks until completion or timeout
    wtc.lastResult = result
    wtc.lastError = err
    if err != nil {
        if errors.Is(err, context.DeadlineExceeded) {
            return fmt.Errorf("workflow '%s' did not complete within %d seconds: %w", wtc.workflowID, timeoutSec, err)
        }
        return fmt.Errorf("workflow '%s' did not complete successfully: %w", wtc.workflowID, err)
    }
    wtc.logger.Info("Workflow completed successfully within timeout", zap.String("ID", wtc.workflowID), zap.Any("Result", result))
    return nil
}


func (wtc *WorkflowTestContext) theWorkflowShouldCompleteWithErrorContaining(errorSubstring string) error {
	if wtc.workflowRun == nil {
		return fmt.Errorf("workflow run handle not available for ID '%s'", wtc.workflowID)
	}
	var result map[string]interface{} // Result might be partial or nil on error
	err := wtc.workflowRun.Get(context.Background(), &result)
	wtc.lastResult = result
	wtc.lastError = err
	if err == nil {
		return fmt.Errorf("workflow '%s' completed successfully, but expected error containing '%s'", wtc.workflowID, errorSubstring)
	}
	if !strings.Contains(err.Error(), errorSubstring) {
		return fmt.Errorf("workflow '%s' error '%v' did not contain expected substring '%s'", wtc.workflowID, err, errorSubstring)
	}
	wtc.logger.Info("Workflow completed with expected error", zap.String("ID", wtc.workflowID), zap.Error(err))
	return nil
}

func (wtc *WorkflowTestContext) theResultForNodeShouldNotBeNil(nodeID string) error {
	if wtc.lastError != nil {
		// If workflow itself failed, checking node results might be misleading unless it's an expected partial result.
		// For now, let this pass if workflow failed, as specific error is checked by another step.
		// return fmt.Errorf("cannot check node result, workflow %s failed: %w", wtc.workflowID, wtc.lastError)
	}
	if wtc.lastResult == nil {
		return fmt.Errorf("workflow result is nil, cannot check node '%s'", nodeID)
	}
	val, ok := wtc.lastResult[nodeID]
	if !ok {
		return fmt.Errorf("result for node '%s' not found in workflow output", nodeID)
	}
	if val == nil {
		return fmt.Errorf("result for node '%s' is nil, but expected not nil", nodeID)
	}
	return nil
}

func (wtc *WorkflowTestContext) theResultForNodeShouldBeNil(nodeID string) error {
	if wtc.lastError != nil {
		// Allow checking nil result even if workflow had an error, as this might be the expected state of a failed/skipped node.
	}
	if wtc.lastResult == nil {
		// This is fine, means the entire result map is nil, so a specific node's result is also effectively nil.
		return nil
	}
	val, ok := wtc.lastResult[nodeID]
	if !ok {
		// If key not present, it's effectively nil for our purposes.
		return nil
	}
	if val != nil {
		return fmt.Errorf("result for node '%s' is '%v', but expected nil", nodeID, val)
	}
	return nil
}

func (wtc *WorkflowTestContext) theResultForNodeShouldBe(nodeID, expectedValue string) error {
	if err := wtc.theResultForNodeShouldNotBeNil(nodeID); err != nil {
		return err
	}
	actualValue := fmt.Sprintf("%v", wtc.lastResult[nodeID]) // Convert to string for comparison
	if actualValue != expectedValue {
		return fmt.Errorf("result for node '%s' is '%s', but expected '%s'", nodeID, actualValue, expectedValue)
	}
	return nil
}

// Placeholder for more complex mocking, actual mocking strategy needs to be decided (e.g. test server with mock activities)
func (wtc *WorkflowTestContext) iOverrideTheActivityToReturn(nodeAndActivityKey string, jsonData *godog.DocString) error {
	// Key: "NodeID::ActivityName" or "ActivityName"
	// This is a simplified mock registration. A real e2e might involve a test worker with mocked activities.
	// For now, this stores an intent that a test worker (not implemented here) would use.
	// Or, if we run against a dev server, this step might be conceptual unless we can hot-swap activities.
	// This step is more for documentation of intent for now.
	wtc.logger.Info("Mocking activity return (conceptual for IT)", zap.String("key", nodeAndActivityKey), zap.String("jsonData", jsonData.Content))
	// wtc.activityMocks[nodeAndActivityKey] = ...
	// This step will be more useful if we use testsuite.TestWorkflowEnvironment for some "integration-like" tests
	// rather than a full external Temporal server where activity code is fixed.
	// For full E2E, this would mean ensuring the deployed worker's activity behaves this way.
	// For now, it's a no-op or stores config for a hypothetical test worker.
	return godog.ErrPending // Mark as pending as full E2E activity mocking is complex
}

func (wtc *WorkflowTestContext) iOverrideTheActivityToExpectParamsContaining(nodeAndActivityKey string, jsonData *godog.DocString) error {
    // Similar to above, this is conceptual for full E2E.
    // It would guide how a mocked activity in a test worker should assert its inputs.
    wtc.logger.Info("Mocking activity param expectation (conceptual for IT)", zap.String("key", nodeAndActivityKey), zap.String("jsonData", jsonData.Content))
    // Store this expectation.
    return godog.ErrPending
}

func (wtc *WorkflowTestContext) iMockActivityAttemptToReturn(nodeAndActivityKey string, attempt int, returnValue string) error {
    wtc.logger.Info("Mocking activity attempt return (conceptual for IT)", zap.String("key", nodeAndActivityKey), zap.Int("attempt", attempt), zap.String("return", returnValue))
    return godog.ErrPending
}

func (wtc *WorkflowTestContext) iOverrideActivityToFailTimesThenSucceed(nodeAndActivityKey string, failCount int, successValue string) error {
    wtc.logger.Info("Mocking activity fail/success (conceptual for IT)", zap.String("key", nodeAndActivityKey), zap.Int("failCount", failCount), zap.String("success", successValue))
    return godog.ErrPending
}

func (wtc *WorkflowTestContext) iMockActivityToSimulateAStartToCloseTimeout(activityKey string) error {
	wtc.logger.Info("Mocking activity timeout (conceptual for IT)", zap.String("key", activityKey))
	return godog.ErrPending
}
func (wtc *WorkflowTestContext) iMockActivityToReturn(activityKey, returnValue string) error {
	wtc.logger.Info("Mocking activity return (conceptual for IT)", zap.String("key", activityKey), zap.String("return", returnValue))
	return godog.ErrPending
}


func (wtc *WorkflowTestContext) activityShouldNotHaveBeenCalled(nodeAndActivityKey string) error {
	// This requires tracking activity calls, complex for true E2E without a test worker.
	// If using testsuite, it's easier. For E2E, we might infer from lack of output or logs.
	wtc.logger.Info("Checking if activity was not called (conceptual for IT)", zap.String("key", nodeAndActivityKey))
	return godog.ErrPending
}

func (wtc *WorkflowTestContext) iWaitForSeconds(seconds int) error {
	time.Sleep(time.Duration(seconds) * time.Second)
	return nil
}

func (wtc *WorkflowTestContext) iWaitForWorkflowToProcessSignalAndAdvanceTimeBySeconds(wfId string, seconds int) error {
    // This step implies that for testsuite based tests, we would use env.AdvanceTime().
    // For E2E against a real server, this is just a time.Sleep().
    // The "process signal" part is implicit in the sleep allowing server to react.
    wtc.logger.Info("Waiting for workflow and advancing time (simulated for E2E by sleep)", zap.String("workflowID", wfId), zap.Int("seconds", seconds))
    time.Sleep(time.Duration(seconds) * time.Second)
    return nil
}

// Step for building config dynamically - useful for timeout/expiry tests
func (wtc *WorkflowTestContext) aWorkflowConfigurationWithANodeHaving(nodeID string, table *godog.Table) error {
    if wtc.workflowConfig.Nodes == nil {
        wtc.workflowConfig.Nodes = make(map[string]shared.Node)
    }
    if wtc.workflowConfig.StartNodeIDs == nil {
        // Assume this node will be a start node unless dependencies are added
        wtc.workflowConfig.StartNodeIDs = []string{nodeID}
    } else {
        // Add as start node if not already present (could be multiple start nodes for a test)
        isStartNode := false
        for _, sn := range wtc.workflowConfig.StartNodeIDs {
            if sn == nodeID {
                isStartNode = true
                break
            }
        }
        if !isStartNode {
            wtc.workflowConfig.StartNodeIDs = append(wtc.workflowConfig.StartNodeIDs, nodeID)
        }
    }


    node := shared.Node{ID: nodeID, Params: make(map[string]interface{})}

    for _, row := range table.Rows {
        property := row.Cells[0].Value
        valueStr := row.Cells[1].Value
        var value interface{}
        // Try to parse value as int, then bool, then leave as string
        if vInt, err := parseInt(valueStr); err == nil {
            value = vInt
        } else if vBool, err := parseBool(valueStr); err == nil {
            value = vBool
        } else {
            value = valueStr
        }

        switch property {
        case "activityName":
            node.ActivityName = valueStr
        case "activityTimeoutSeconds":
            if v, ok := value.(int); ok { node.ActivityTimeoutSeconds = v } else { return fmt.Errorf("activityTimeoutSeconds must be int for %s", nodeID)}
        case "expirySeconds":
             if v, ok := value.(int); ok { node.ExpirySeconds = v } else { return fmt.Errorf("expirySeconds must be int for %s", nodeID)}
        case "nextNodeRules": // Example: "AlwaysTrue -> DependentNode"
            parts := strings.Split(valueStr, "->")
            if len(parts) != 2 { return fmt.Errorf("invalid nextNodeRule format: '%s'", valueStr)}
            node.NextNodeRules = append(node.NextNodeRules, shared.NextNodeRule{
                RuleID: strings.TrimSpace(parts[0]),
                TargetNodeID: strings.TrimSpace(parts[1]),
            })
        // Add more properties as needed by tests (type, signalName, resultValiditySeconds, etc.)
        default:
            // Store other properties in Params or handle them if they are direct fields
            node.Params[property] = value
        }
    }
    wtc.workflowConfig.Nodes[nodeID] = node
    return nil
}

func (wtc *WorkflowTestContext) nodeWhichDependsOnAndRuns(nodeID, dependencyNodeID, activityName string) error {
    if wtc.workflowConfig.Nodes == nil {
        wtc.workflowConfig.Nodes = make(map[string]shared.Node)
    }
    node := shared.Node{
        ID: nodeID,
        ActivityName: activityName,
        Dependencies: []string{dependencyNodeID},
        Params: make(map[string]interface{}),
    }
    wtc.workflowConfig.Nodes[nodeID] = node
    return nil
}


// Helper parsers
func parseInt(s string) (int, error) {
    var i int
    _, err := fmt.Sscan(s, &i)
    return i, err
}

func parseBool(s string) (bool, error) {
    var b bool
    _, err := fmt.Sscan(s, &b)
    return b, err
}
