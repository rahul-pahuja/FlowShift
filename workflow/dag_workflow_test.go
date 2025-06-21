package workflow

import (
	"context" // Required for mock function signatures
	"dynamicworkflow/activities"
	"dynamicworkflow/shared"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/api/enums/v1" // For enumspb
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

type UnitTestSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
	env *testsuite.TestWorkflowEnvironment
}

func TestUnitTestSuite(t *testing.T) {
	suite.Run(t, new(UnitTestSuite))
}

func (s *UnitTestSuite) SetupTest() {
	s.env = s.NewTestWorkflowEnvironment()
	// Register real activities; mocks in tests will override behavior for specific calls.
	s.env.RegisterActivity(activities.SimpleSyncActivity)
	s.env.RegisterActivity(activities.AsyncActivityExample)
	s.env.RegisterActivity(activities.WaitActivityExample)
	s.env.RegisterActivity(activities.ActivityThatCanFail)
	// Register any placeholder activities used in tests if not actual ones
	s.env.RegisterActivity(func(ctx context.Context, params map[string]interface{}) (string, error) { return "LongRunningOutput", nil }) // LongRunningActivity
	s.env.RegisterActivity(func(ctx context.Context, params map[string]interface{}) (string, error) { return "ShortActivityOutput", nil }) // ShortActivity
	s.env.RegisterActivity(func(ctx context.Context, params map[string]interface{}) (string, error) { return "NeverRunOutput", nil })    // NeverRunActivity
}

func (s *UnitTestSuite) AfterTest(suiteName, testName string) {
	s.env.AssertExpectations(s.T())
	s.env.Close()
}

// --- Test Cases (Existing tests updated, new tests to be added) ---

func (s *UnitTestSuite) Test_BasicDAG_SuccessfulExecution_WithNextNodeRules() {
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{"A"},
		Nodes: map[string]shared.Node{
			"A": {
				ID:           "A",
				ActivityName: "SimpleSyncActivity",
				Params:       map[string]interface{}{"data": "A_data"},
				NextNodeRules: []shared.NextNodeRule{
					{Condition: "output.status == 'done'", TargetNodeID: "B"},
					{Condition: "output.status == 'done'", TargetNodeID: "C"},
				},
			},
			"B": {ID: "B", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"data": "B_data"}, Dependencies: []string{"A"}, NextNodeRules: []shared.NextNodeRule{{Condition: "true", TargetNodeID: "D"}}},
			"C": {ID: "C", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"data": "C_data"}, Dependencies: []string{"A"}, NextNodeRules: []shared.NextNodeRule{{Condition: "true", TargetNodeID: "D"}}},
			"D": {ID: "D", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"data": "D_data"}, Dependencies: []string{"B", "C"}},
		},
	}
	input := DAGWorkflowInput{Config: config}

	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "A_data"}).Return(map[string]interface{}{"status": "done"}, nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "B_data"}).Return("Output from B", nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "C_data"}).Return("Output from C", nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "D_data"}).Return("Output from D", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.NotNil(results["A"])
	s.Equal("Output from B", results["B"])
	s.Equal("Output from C", results["C"])
	s.Equal("Output from D", results["D"])
}

func (s *UnitTestSuite) Test_NodeSkipped_WithNextNodeRules() {
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{"A"},
		Nodes: map[string]shared.Node{
			"A":      {ID: "A", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"data": "A_data"}, NextNodeRules: []shared.NextNodeRule{{Condition: "true", TargetNodeID: "B_skip"}}},
			"B_skip": {ID: "B_skip", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"_skip": true}, Dependencies: []string{"A"}, NextNodeRules: []shared.NextNodeRule{{Condition: "true", TargetNodeID: "C"}}},
			"C":      {ID: "C", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"data": "C_data"}, Dependencies: []string{"B_skip"}},
		},
	}
	input := DAGWorkflowInput{Config: config}
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "A_data"}).Return("Output from A", nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "C_data"}).Return("Output from C", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Equal("SKIPPED", results["B_skip"])
	s.Equal("Output from C", results["C"])
}

func (s *UnitTestSuite) Test_NodeFailure_RedoSuccess_WithActivityTimeoutSeconds() {
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{"FailNode"},
		Nodes: map[string]shared.Node{
			"FailNode": {ID: "FailNode", ActivityName: "ActivityThatCanFail", Params: map[string]interface{}{"id": "FailNode"}, RedoCondition: "on_failure", ActivityTimeoutSeconds: 10, NextNodeRules: []shared.NextNodeRule{{Condition: "true", TargetNodeID: "SuccessNode"}}},
			"SuccessNode": {ID: "SuccessNode", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"id": "SuccessNode"}, Dependencies: []string{"FailNode"}},
		},
	}
	input := DAGWorkflowInput{Config: config}
	s.env.OnActivity("ActivityThatCanFail", mock.Anything, mock.Anything).Return("", errors.New("simulated error")).Once()
	s.env.OnActivity("ActivityThatCanFail", mock.Anything, mock.Anything).Return("Output from FailNode (2nd attempt)", nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.Anything).Return("Output from SuccessNode", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
}

func (s *UnitTestSuite) Test_NodeActivityTimeout() {
	nodeID, depID := "TimeoutNode", "DependentNode"
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{nodeID},
		Nodes: map[string]shared.Node{
			nodeID: {ID: nodeID, ActivityName: "LongRunningActivity", ActivityTimeoutSeconds: 1, NextNodeRules: []shared.NextNodeRule{{Condition: "true", TargetNodeID: depID}}},
			depID:  {ID: depID, ActivityName: "SimpleSyncActivity", Dependencies: []string{nodeID}},
		},
	}
	input := DAGWorkflowInput{Config: config}
	s.env.OnActivity("LongRunningActivity", mock.Anything, mock.Anything).Return("", temporal.NewTimeoutError("simulated STC", enumspb.TIMEOUT_TYPE_START_TO_CLOSE)).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.Anything).Return("Output from DependentNode", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	err := s.env.GetWorkflowError()
	s.Error(err)
	if err != nil {
		s.Contains(err.Error(), fmt.Sprintf("node %s %s", nodeID, shared.NodeStateTimedOut))
	}
}

func (s *UnitTestSuite) Test_NodeExpiry_BeforeActivityStart() {
	s.env.SetStartTime(time.Now())
	nodeA, nodeToExpire, nodeC := "A", "ExpireNode", "C"
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{nodeA},
		Nodes: map[string]shared.Node{
			nodeA:        {ID: nodeA, ActivityName: "ShortActivity", NextNodeRules: []shared.NextNodeRule{{Condition: "true", TargetNodeID: nodeToExpire}}},
			nodeToExpire: {ID: nodeToExpire, ActivityName: "NeverRunActivity", Dependencies: []string{nodeA}, ExpirySeconds: 2, NextNodeRules: []shared.NextNodeRule{{Condition: "true", TargetNodeID: nodeC}}},
			nodeC:        {ID: nodeC, ActivityName: "SimpleSyncActivity", Dependencies: []string{nodeToExpire}},
		},
	}
	input := DAGWorkflowInput{Config: config}
	s.env.OnActivity("ShortActivity", mock.Anything, mock.Anything).Return("Output from A", nil).Once()
	s.env.RegisterDelayedCallback(func() { s.env.AdvanceTime(3 * time.Second) }, 1*time.Millisecond)
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.Anything).Return("Output from C", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Nil(results[nodeToExpire])
	s.Equal("Output from C", results[nodeC])
}

// --- New Test Cases ---

func (s *UnitTestSuite) Test_ConditionalPath_Path1Taken() {
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{"Start"},
		Nodes: map[string]shared.Node{
			"Start": {ID: "Start", ActivityName: "DecisionActivity", Params: map[string]interface{}{"choice": "path1"},
				NextNodeRules: []shared.NextNodeRule{
					{Condition: "output.decision == 'PATH1'", TargetNodeID: "Path1Node"},
					{Condition: "output.decision == 'PATH2'", TargetNodeID: "Path2Node"},
				}},
			"Path1Node": {ID: "Path1Node", ActivityName: "Path1Activity", Dependencies: []string{"Start"}},
			"Path2Node": {ID: "Path2Node", ActivityName: "Path2Activity", Dependencies: []string{"Start"}},
		},
	}
	input := DAGWorkflowInput{Config: config}
	s.env.OnActivity("DecisionActivity", mock.Anything, mock.Anything).Return(map[string]interface{}{"decision": "PATH1"}, nil).Once()
	s.env.OnActivity("Path1Activity", mock.Anything, mock.Anything).Return("Path1 Output", nil).Once()
	// Path2Activity should not be called

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Equal("Path1 Output", results["Path1Node"])
	s.Nil(results["Path2Node"])
}

func (s *UnitTestSuite) Test_UserInputNode_ReceivesSignal() {
	nodeID := "InputNode"
	signalName := "MyTestSignal"
	signalPayload := map[string]interface{}{"signal_data": "important_value"}

	config := shared.WorkflowConfig{
		StartNodeIDs: []string{nodeID},
		Nodes: map[string]shared.Node{
			nodeID: {
				ID:           nodeID,
				Type:         shared.NodeTypeUserInput,
				ActivityName: "PostSignalActivity",
				SignalName:   signalName,
				Params:       map[string]interface{}{"original_param": "original_value"},
			},
		},
	}
	input := DAGWorkflowInput{Config: config}

	// Activity is called after signal, with merged params
	expectedParams := map[string]interface{}{"original_param": "original_value", "signal_data": "important_value"}
	s.env.OnActivity("PostSignalActivity", mock.Anything, expectedParams).Return("Activity Done", nil).Once()

	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(signalName, signalPayload)
	}, 1*time.Second) // Send signal after workflow starts and is waiting

	s.env.ExecuteWorkflow(DAGWorkflow, input)

	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Equal("Activity Done", results[nodeID])
}

func (s *UnitTestSuite) Test_ResultValidity_DependentCompletesInTime() {
	sourceNodeID := "SourceValidityNode"
	dependentNodeID := "DependentSignalNode"
	sourceSignal := "SourceSignal"
	dependentSignal := "DependentSignal"

	config := shared.WorkflowConfig{
		StartNodeIDs: []string{sourceNodeID},
		Nodes: map[string]shared.Node{
			sourceNodeID: {
				ID:                    sourceNodeID,
				Type:                  shared.NodeTypeUserInput,
				ActivityName:          "SimpleSyncActivity",
				SignalName:            sourceSignal,
				ResultValiditySeconds: 5, // Result valid for 5 virtual seconds
				Params:                map[string]interface{}{"id": sourceNodeID},
				NextNodeRules:         []shared.NextNodeRule{{Condition: "true", TargetNodeID: dependentNodeID}},
			},
			dependentNodeID: {
				ID:           dependentNodeID,
				Type:         shared.NodeTypeUserInput,
				ActivityName: "SimpleSyncActivity",
				SignalName:   dependentSignal,
				Params:       map[string]interface{}{"id": dependentNodeID},
				Dependencies: []string{sourceNodeID},
			},
		},
	}
	input := DAGWorkflowInput{Config: config}

	// Source node activity
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"id": sourceNodeID, "signal_data_source": "src_done"}).Return("SourceOutput", nil).Once()
	// Dependent node activity
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"id": dependentNodeID, "signal_data_dependent": "dep_done"}).Return("DependentOutput", nil).Once()

	// Schedule signals
	s.env.RegisterDelayedCallback(func() { // Signal for SourceNode
		s.env.SignalWorkflow(sourceSignal, map[string]interface{}{"signal_data_source": "src_done"})
	}, 1*time.Second)

	s.env.RegisterDelayedCallback(func() { // Signal for DependentNode, well within validity period
		s.env.SignalWorkflow(dependentSignal, map[string]interface{}{"signal_data_dependent": "dep_done"})
		s.env.AdvanceTime(1 * time.Second) // Ensure dependent processing happens
	}, 2*time.Second) // Send this after source signal, but before validity (5s) expires

	s.env.ExecuteWorkflow(DAGWorkflow, input)

	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Equal("SourceOutput", results[sourceNodeID])
	s.Equal("DependentOutput", results[dependentNodeID])
	// Implicitly, test passes if no redo occurs and workflow completes normally.
}

func (s *UnitTestSuite) Test_ResultValidity_TimerFires_ResetsNodes() {
	sourceNodeID := "SourceStaleNode"
	dependentNodeID := "DependentWaitsTooLongNode"
	sourceSignal := "SourceStaleSignal"
	dependentSignal := "Dep αργείSignal" // Greek for "late" :)

	config := shared.WorkflowConfig{
		StartNodeIDs: []string{sourceNodeID},
		Nodes: map[string]shared.Node{
			sourceNodeID: {
				ID:                    sourceNodeID,
				Type:                  shared.NodeTypeUserInput,
				ActivityName:          "SimpleSyncActivity", // Activity name for the source
				SignalName:            sourceSignal,
				ResultValiditySeconds: 2, // Short validity
				Params:                map[string]interface{}{"id": sourceNodeID, "attempt": 1},
				NextNodeRules:         []shared.NextNodeRule{{Condition: "true", TargetNodeID: dependentNodeID}},
			},
			dependentNodeID: {
				ID:           dependentNodeID,
				Type:         shared.NodeTypeUserInput,
				ActivityName: "SimpleSyncActivity", // Activity name for the dependent
				SignalName:   dependentSignal,
				Params:       map[string]interface{}{"id": dependentNodeID, "attempt": 1},
				Dependencies: []string{sourceNodeID},
			},
		},
	}
	input := DAGWorkflowInput{Config: config}

	// --- Attempt 1 Mocks ---
	// Source node (1st attempt) - activity completes
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"id": sourceNodeID, "attempt": 1, "signal_for_attempt1_source": true}).Return("SourceOutput_Attempt1", nil).Once()
	// Dependent node (1st attempt) - its signal will be delayed, so its activity won't run in 1st attempt.

	// --- Attempt 2 Mocks (after reset due to validity timer) ---
	// Source node (2nd attempt) - activity completes
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"id": sourceNodeID, "attempt": 2, "signal_for_attempt2_source": true}).Return("SourceOutput_Attempt2", nil).Once()
	// Dependent node (2nd attempt) - activity completes
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"id": dependentNodeID, "attempt": 1, "signal_for_attempt2_dependent": true}).Return("DependentOutput_Attempt2", nil).Once()


	// Workflow Execution
	s.env.ExecuteWorkflow(DAGWorkflow, input) // Start workflow

	// Signal for SourceNode (Attempt 1)
	s.env.SignalWorkflow(sourceSignal, map[string]interface{}{"signal_for_attempt1_source": true})
	s.env.AssertExpectations(s.T()) // Check if source activity (1st attempt) ran

	// Advance time PAST ResultValiditySeconds (2s) + some buffer
	s.env.AdvanceTime(3 * time.Second) // This should trigger the timer and reset.

	// Now, signal for the nodes again (Attempt 2 for source, Attempt 1 for dependent but effectively 2nd round)
	s.env.SignalWorkflow(sourceSignal, map[string]interface{}{"signal_for_attempt2_source": true, "attempt": 2}) // Send attempt in signal for mock
	s.env.AssertExpectations(s.T()) // Check source activity (2nd attempt)

	s.env.SignalWorkflow(dependentSignal, map[string]interface{}{"signal_for_attempt2_dependent": true, "attempt": 1})// Send attempt in signal for mock
	s.env.AssertExpectations(s.T()) // Check dependent activity (2nd attempt)

	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())

	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Equal("SourceOutput_Attempt2", results[sourceNodeID])
	s.Equal("DependentOutput_Attempt2", results[dependentNodeID])
}

// Mock activity names used in configs if they aren't real activity functions
// For simplicity, some tests use generic names like "Path1Activity".
// If these were actual activities, they'd be registered from activities package.
// For testsuite, if a mock is set up for an activity name, it doesn't strictly need
// a real registered function, but it's good practice if possible.
// Placeholder registration for activities only named in tests:
func init() {
	// This is a bit of a hack for test suite. Usually, you register actual functions.
	// If an activity is *only* mocked and never called "for real", this isn't strictly needed.
	// But if a test config uses an activity name that has no real counterpart and isn't mocked,
	// the workflow would fail to find it.
	activity.Register(func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) { return map[string]interface{}{"decision": "PATH1"}, nil }, "DecisionActivity")
	activity.Register(func(ctx context.Context) (string, error) { return "Path1 Output", nil }, "Path1Activity")
	activity.Register(func(ctx context.Context) (string, error) { return "Path2 Output", nil }, "Path2Activity")
	activity.Register(func(ctx context.Context, params map[string]interface{}) (string, error) { return "Activity Done", nil }, "PostSignalActivity")
}
