package workflow

import (
	"errors"
	"fmt"
	"time"

	"dynamicworkflow/shared"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap" // For logging
)

// DAGWorkflowInput is the input to the main DAG workflow
type DAGWorkflowInput struct {
	Config shared.WorkflowConfig
}

// DAGWorkflow executes a dynamic DAG based on the provided configuration
func DAGWorkflow(ctx workflow.Context, input DAGWorkflowInput) (map[string]interface{}, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("DAGWorkflow started", zap.Any("input", input))

	config := input.Config
	nodeStatuses := make(map[string]*shared.NodeStatus)
	nodeResults := make(map[string]interface{})

	// Initialize statuses for all nodes
	for id := range config.Nodes {
		nodeStatuses[id] = &shared.NodeStatus{
			State:   shared.NodeStatePending,
			Attempt: 1,
		}
	}

	// --- DAG Processing Logic ---

	// Build reverse dependency map (dependentsMap) and initial dependency counts
	dependentsMap := make(map[string][]string) // nodeID -> list of nodes that depend on it
	dependencyCount := make(map[string]int)   // nodeID -> number of outstanding dependencies

	for id, node := range config.Nodes {
		dependencyCount[id] = len(node.Dependencies)
		for _, depID := range node.Dependencies {
			dependentsMap[depID] = append(dependentsMap[depID], id)
		}
	}

	// Ready queue: nodes with all dependencies met
	readyQueue := make(chan string, len(config.Nodes)) // Buffered channel

	// Initialize ready queue with start nodes or nodes with no dependencies
	for _, id := range config.StartNodeIDs {
		if _, ok := config.Nodes[id]; ok {
			if dependencyCount[id] == 0 { // Should be true for start nodes if properly configured
				logger.Info("Adding start node to ready queue", zap.String("NodeID", id))
				nodeStatuses[id].ScheduledTime = workflow.Now(ctx).Unix()
				readyQueue <- id
			} else {
				logger.Warn("StartNodeID has unmet dependencies initially, check config", zap.String("NodeID", id), zap.Int("Deps", dependencyCount[id]))
				// Or handle this as an error depending on desired strictness
			}
		} else {
			logger.Warn("Invalid StartNodeID defined in config", zap.String("NodeID", id))
		}
	}
	// Also add any node that has no dependencies, even if not in StartNodeIDs (could be parallel starts)
	for id, count := range dependencyCount {
		if count == 0 {
			// Avoid double-adding if already in StartNodeIDs and processed
			isStartNode := false
			for _, startID := range config.StartNodeIDs {
				if id == startID {
					isStartNode = true
					break
				}
			}
			if !isStartNode {
				logger.Info("Adding node with no dependencies to ready queue", zap.String("NodeID", id))
				nodeStatuses[id].ScheduledTime = workflow.Now(ctx).Unix()
				readyQueue <- id
			}
		}
	}


	numTotalNodes := len(config.Nodes)
	numCompletedOrSkippedNodes := 0
	numProcessingNodes := 0 // Nodes currently being processed by a goroutine

	// Selector for managing concurrent operations
	selector := workflow.NewSelector(ctx)
	var errAggregator error // To collect errors from activities

	// Main loop: continues as long as there are nodes to process or being processed
	for numCompletedOrSkippedNodes < numTotalNodes || numProcessingNodes > 0 {
		// Add futures for nodes in the ready queue to the selector
		// Only add if we have capacity or if there are items in the ready queue
		select {
		case nodeID := <-readyQueue:
			numProcessingNodes++
			node := config.Nodes[nodeID] // Assume nodeID is valid
			status := nodeStatuses[nodeID]

			logger.Info("Preparing to process node", zap.String("NodeID", nodeID))

			// --- Skip Logic ---
			// Example: Primitive skip based on a parameter.
			// A real implementation might use an expression evaluator against workflow data.
			shouldSkip := false
			if skipVal, ok := node.Params["_skip"].(bool); ok && skipVal {
				// Or evaluate node.SkipCondition string here
				shouldSkip = true
			}
			// More advanced: Implement evaluation for node.SkipCondition
			// e.g., if node.SkipCondition == "custom_condition_true(params)"
			//      shouldSkip = evaluateCustomCondition(node.Params, workflowData)


			if shouldSkip {
				logger.Info("Skipping node due to SkipCondition", zap.String("NodeID", nodeID), zap.String("condition", node.SkipCondition))
				status.State = shared.NodeStateSkipped
				status.EndTime = workflow.Now(ctx).Unix() // Mark with a time
				nodeResults[nodeID] = "SKIPPED" // Or some specific marker
				numCompletedOrSkippedNodes++
				numProcessingNodes-- // Decrement as we are not actually processing it via activity

				// Propagate "completion" (as skipped) to dependents
				for _, dependentID := range dependentsMap[nodeID] {
					dependencyCount[dependentID]--
					if dependencyCount[dependentID] == 0 && nodeStatuses[dependentID].State == shared.NodeStatePending {
						logger.Info("All dependencies met for node (after one skipped), adding to ready queue", zap.String("NodeID", dependentID))
						nodeStatuses[dependentID].ScheduledTime = workflow.Now(ctx).Unix()
						readyQueue <- dependentID
					}
				}
				return // Do not proceed to expiry check or execution for this skipped node
			}

			// If not skipped, proceed to set state to Running and check expiry etc.
			status.State = shared.NodeStateRunning
			status.StartTime = workflow.Now(ctx).Unix()

			// --- Node Expiry Check ---
			if node.ExpirySeconds > 0 {
				expiryDuration := time.Duration(node.ExpirySeconds) * time.Second
				scheduledTime := time.Unix(status.ScheduledTime, 0)
				if workflow.Now(ctx).After(scheduledTime.Add(expiryDuration)) {
					logger.Warn("Node expired before execution",
						zap.String("NodeID", nodeID),
						zap.Time("scheduledTime", scheduledTime),
						zap.Duration("expiryDuration", expiryDuration))
					status.State = shared.NodeStateExpired
					status.EndTime = workflow.Now(ctx).Unix()
					status.Error = fmt.Errorf("node %s expired before execution", nodeID)
					nodeResults[nodeID] = nil // Or some specific marker for expiry
					numCompletedOrSkippedNodes++
					numProcessingNodes-- // Decrement as we are not actually processing it via activity

					// Crucially, we still need to "complete" this node in terms of DAG progression
					// so its dependents can be evaluated (they might not run if they depend on expired node's output,
					// but the DAG structure needs to be respected).
					// If an expired node should halt dependent execution, that's a higher-level config/logic.
					// For now, dependents of an expired node will have their dependency count reduced.
					// This behavior might need refinement based on exact requirements.
					for _, dependentID := range dependentsMap[nodeID] {
						dependencyCount[dependentID]--
						if dependencyCount[dependentID] == 0 && nodeStatuses[dependentID].State == shared.NodeStatePending {
							logger.Info("All dependencies met for node (after one expired), adding to ready queue", zap.String("NodeID", dependentID))
							nodeStatuses[dependentID].ScheduledTime = workflow.Now(ctx).Unix()
							readyQueue <- dependentID
						}
					}
					return // Skip activity execution for this expired node
				}
			}

			// --- Activity Options with TTL ---
			activityOpts := workflow.ActivityOptions{
				StartToCloseTimeout: 60 * time.Minute, // Default TTL if not specified
				HeartbeatTimeout:    0,                // Optional: set if activities heartbeat
				// RetryPolicy can be set here if not handled by specific redo logic
			}
			if node.TTLSeconds > 0 {
				activityOpts.StartToCloseTimeout = time.Duration(node.TTLSeconds) * time.Second
			}
			// For activities that heartbeat (like WaitActivityExample), set HeartbeatTimeout
			// This should ideally be based on the activity type or specific node config.
			// For now, a generic check:
			if node.Type == shared.NodeTypeWait || node.ActivityName == "WaitActivityExample" { // Example condition
				activityOpts.HeartbeatTimeout = 20*time.Second // Must be longer than heartbeat interval in activity
				logger.Info("Setting HeartbeatTimeout for activity", zap.String("NodeID", nodeID), zap.Duration("timeout", activityOpts.HeartbeatTimeout))
			}

			currentCtx := workflow.WithActivityOptions(ctx, activityOpts)
			future := workflow.ExecuteActivity(currentCtx, node.ActivityName, node.Params)

			selector.AddFuture(future, func(f workflow.Future) {
				numProcessingNodes--
				var activityOutput interface{}
				err := f.Get(ctx, &activityOutput)
				status.EndTime = workflow.Now(ctx).Unix()

				if err != nil {
					logger.Error("Activity execution failed", zap.String("NodeID", nodeID), zap.Error(err))
					// Check for TTL timeout
					var timeoutErr *workflow.TimeoutError
					if workflow.IsTimeoutError(err) && errors.As(err, &timeoutErr) && timeoutErr.TimeoutType() == enumspb.TIMEOUT_TYPE_START_TO_CLOSE {
						status.State = shared.NodeStateTimedOut
						logger.Warn("Activity timed out (TTL)", zap.String("NodeID", nodeID), zap.Error(err))
					} else {
						status.State = shared.NodeStateFailed
					}
					status.Error = err

					// --- Redo Logic ---
					maxRedoAttempts := 3 // Max attempts for nodes with redo enabled
					shouldRedo := false
					if status.State == shared.NodeStateFailed && // Only redo actual failures, not TTL timeouts
						node.RedoCondition == "on_failure" &&
						status.Attempt < maxRedoAttempts {

						shouldRedo = true
					}

					if shouldRedo {
						logger.Info("Node failed, attempting redo",
							zap.String("NodeID", nodeID),
							zap.Int("attempt", status.Attempt),
							zap.Int("maxAttempts", maxRedoAttempts))
						status.Attempt++
						status.State = shared.NodeStatePending // Reset state for re-processing
						status.Error = nil                    // Clear previous error
						status.ScheduledTime = workflow.Now(ctx).Unix()
						// Add back to the ready queue to be picked up again.
						// This assumes the failure was transient or the next attempt might succeed.
						// It will go through expiry checks again if applicable.
						readyQueue <- nodeID
						// numProcessingNodes was already decremented. It will be incremented again when picked from readyQueue.
						// numCompletedOrSkippedNodes is NOT incremented as the node is not yet done.
					} else {
						if status.State == shared.NodeStateFailed && node.RedoCondition == "on_failure" && status.Attempt >= maxRedoAttempts {
							logger.Warn("Node failed, max redo attempts reached",
								zap.String("NodeID", nodeID),
								zap.Int("attempt", status.Attempt))
						}
						// Mark as completed (failed or timedout) to allow DAG to proceed or terminate
						numCompletedOrSkippedNodes++
						if errAggregator == nil { // Store first error
							errAggregator = fmt.Errorf("node %s %s: %w (attempt %d)", nodeID, status.State, err, status.Attempt)
						}
						// If not redoing, then we propagate to dependents
						for _, dependentID := range dependentsMap[nodeID] {
							dependencyCount[dependentID]--
							if dependencyCount[dependentID] == 0 && nodeStatuses[dependentID].State == shared.NodeStatePending {
								logger.Info("All dependencies met for node (after one failed/timedout), adding to ready queue", zap.String("NodeID", dependentID))
								nodeStatuses[dependentID].ScheduledTime = workflow.Now(ctx).Unix()
								readyQueue <- dependentID
							}
						}
					}
				} else {
					logger.Info("Activity execution completed", zap.String("NodeID", nodeID), zap.Any("Output", activityOutput))
					status.State = shared.NodeStateCompleted
					status.Result = activityOutput
					nodeResults[nodeID] = activityOutput
					numCompletedOrSkippedNodes++

					// Propagate completion to dependents
					for _, dependentID := range dependentsMap[nodeID] {
						dependencyCount[dependentID]--
						if dependencyCount[dependentID] == 0 && nodeStatuses[dependentID].State == shared.NodeStatePending {
							logger.Info("All dependencies met for node, adding to ready queue", zap.String("NodeID", dependentID))
							nodeStatuses[dependentID].ScheduledTime = workflow.Now(ctx).Unix()
							readyQueue <- dependentID
						}
					}
				}
			})
		default:
			// If readyQueue is empty, block on selector.Select(ctx)
			if numProcessingNodes > 0 || len(readyQueue) > 0 { // only select if there's work or potential work
				selector.Select(ctx) // Process one completed activity
			} else if numCompletedOrSkippedNodes < numTotalNodes && len(readyQueue) == 0 && numProcessingNodes == 0 {
				// This condition means no nodes are running, none are ready, but not all are done.
				// This could indicate a deadlock/cycle in the DAG or an issue with logic.
				logger.Error("Potential deadlock or orphaned nodes in DAG",
					zap.Int("completedOrSkipped", numCompletedOrSkippedNodes),
					zap.Int("totalNodes", numTotalNodes),
					zap.Int("processing", numProcessingNodes),
					zap.Int("readyQueueSize", len(readyQueue)))
				// To prevent spinning, we can wait for a short duration or a signal
				// For now, break to avoid infinite loop in this scenario.
				// A more robust solution would be to identify the stuck nodes.
				if errAggregator == nil {
					errAggregator = fmt.Errorf("DAG execution stalled with %d/%d nodes completed and no ready/processing nodes", numCompletedOrSkippedNodes, numTotalNodes)
				}
				goto endLoop // Exit main processing loop
			} else {
				// All nodes processed or nothing to do.
				goto endLoop
			}
		}
	}

endLoop:
	if numCompletedOrSkippedNodes < numTotalNodes && errAggregator == nil {
		logger.Warn("Workflow finished but not all nodes completed.",
			zap.Int("completedOrSkipped", numCompletedOrSkippedNodes),
			zap.Int("total", numTotalNodes))
		errAggregator = fmt.Errorf("workflow incomplete: %d/%d nodes processed", numCompletedOrSkippedNodes, numTotalNodes)
	} else if errAggregator != nil {
		logger.Error("DAGWorkflow finished with errors.", zap.Error(errAggregator))
	} else {
		logger.Info("DAGWorkflow finished successfully.")
	}

	return nodeResults, errAggregator
}
