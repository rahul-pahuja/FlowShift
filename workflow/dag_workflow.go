THIS SHOULD BE A LINTER ERRORpackage workflow

import (
	"errors"
	"fmt"
	"time"

	"flow-shift/shared"
	enumspb "go.temporal.io/api/enums/v1"
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

	// For ResultValiditySeconds: tracks timers for nodes whose results have a validity period.
	type activeValidityTimerInfo struct {
		timerID             string // usually sourceNodeID
		sourceNodeID        string
		dependentNodeID     string // The specific dependent whose completion defuses the timer
		timerFuture         workflow.Future
		timerCancelFunc     workflow.CancelFunc
	}
	activeValidityTimers := make(map[string]*activeValidityTimerInfo) // timerID -> info

	// Helper to find the "critical" dependent for a source node's ResultValidity.
	// This is simplified: assumes the first UserInput node found in NextNodeRules triggered by a "success" condition,
	// or the first UserInput node found in static dependents.
	// A more robust mechanism would involve explicit configuration.
	findCriticalDependent := func(sourceNodeID string, sourceNode shared.Node, sourceOutput interface{}) (string, bool) {
		if len(sourceNode.NextNodeRules) > 0 {
			for _, rule := range sourceNode.NextNodeRules {
				if actualRule, ok := config.Rules[rule.RuleID]; ok && evaluateCondition(ctx, actualRule.Expression, sourceNode.Params, sourceOutput) {
					if targetNode, ok := config.Nodes[rule.TargetNodeID]; ok && targetNode.Type == shared.NodeTypeUserInput {
						// Check if this dependent also depends *only* on the sourceNode to avoid complex multi-parent validity issues for now
						isSimpleDependent := true
						if len(targetNode.Dependencies) > 1 {
							// For simplicity, only handle cases where the user_input dependent has the source as its *sole* dependency for validity tracking.
							// logger.Debug("Critical dependent candidate has multiple dependencies, too complex for current validity tracking", zap.String("Source", sourceNodeID), zap.String("Target", targetNode.ID))
							// isSimpleDependent = false
							// Let's allow it for now, but be aware of complexity. The timer is for sourceNode's result validity.
						}
						if isSimpleDependent {
							return rule.TargetNodeID, true
						}
					}
				}
			}
		} else { // Fallback to static dependents
			for _, depID := range dependentsMap[sourceNodeID] {
				if depNode, ok := config.Nodes[depID]; ok && depNode.Type == shared.NodeTypeUserInput {
					// Similar simplicity check for static dependents
					isSimpleDependent := true
					if len(depNode.Dependencies) > 1 {
						// isSimpleDependent = false
					}
					if isSimpleDependent {
						return depID, true
					}
				}
			}
		}
		return "", false
	}


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

			// --- UserInput Node Type: Wait for Signal ---
			var signalData interface{} // To store data received from signal
			if node.Type == shared.NodeTypeUserInput {
				if node.SignalName == "" {
					logger.Error("UserInput node type requires a SignalName", zap.String("NodeID", nodeID))
					status.State = shared.NodeStateFailed
					status.Error = fmt.Errorf("userInput node %s missing SignalName configuration", nodeID)
					status.EndTime = workflow.Now(ctx).Unix()
					numCompletedOrSkippedNodes++
					numProcessingNodes--
					// Propagate failure to allow DAG to potentially continue or terminate gracefully
					if errAggregator == nil {
						errAggregator = status.Error
					}
					// TODO: Consider if dependents should be processed here or if this failure halts the path
					return // Exit this goroutine for the node
				}

				logger.Info("Node is UserInput type, waiting for signal", zap.String("NodeID", nodeID), zap.String("SignalName", node.SignalName))
				status.State = shared.NodeStatePending // Or a new state e.g., NodeStateWaitingForSignal
				// Note: numProcessingNodes is already incremented. This node is actively "processing" by waiting.

				signalChan := workflow.GetSignalChannel(ctx, node.SignalName)
				signalReceived := false

				// Loop with select to allow timer for expiry while waiting for signal
				// This is a simplified expiry check during signal wait. A more robust one might involve a dedicated timer future in the main selector.
				var expiryTimerFuture workflow.Future
				if node.ExpirySeconds > 0 {
					scheduledTime := time.Unix(status.ScheduledTime, 0) // Time it became ready for signal
					expiryDuration := time.Duration(node.ExpirySeconds) * time.Second
					timeUntilExpiry := scheduledTime.Add(expiryDuration).Sub(workflow.Now(ctx))
					if timeUntilExpiry > 0 {
						expiryTimerFuture = workflow.NewTimer(ctx, timeUntilExpiry)
					} else {
						// Already expired
						logger.Warn("UserInput node expired while attempting to wait for signal", zap.String("NodeID", nodeID))
						status.State = shared.NodeStateExpired
						status.Error = fmt.Errorf("userInput node %s expired before signal could be set up", nodeID)
						// ... (standard expiry handling from below, simplified here)
						numCompletedOrSkippedNodes++
						numProcessingNodes--
						if errAggregator == nil { errAggregator = status.Error }
						// TODO: Propagate to dependents if necessary for this type of expiry
						return nodeResults, errAggregator
					}
				}

				selectorForSignal := workflow.NewSelector(ctx)
				selectorForSignal.AddReceive(signalChan, func(c workflow.Channel, more bool) {
					if !more { // Channel closed
						logger.Warn("Signal channel closed for UserInput node", zap.String("NodeID", nodeID), zap.String("SignalName", node.SignalName))
						status.Error = fmt.Errorf("signal channel %s closed for node %s", node.SignalName, nodeID)
						// This will lead to failure path below
						return
					}
					c.Receive(ctx, &signalData)
					logger.Info("Signal received for UserInput node", zap.String("NodeID", nodeID), zap.String("SignalName", node.SignalName), zap.Any("SignalData", signalData))
					signalReceived = true
					if expiryTimerFuture != nil { // Cancel expiry timer if signal received
						expiryTimerFuture.Cancel()
					}
				})

				if expiryTimerFuture != nil {
					selectorForSignal.AddFuture(expiryTimerFuture, func(f workflow.Future) {
						// Timer fired, meaning node expired while waiting for signal
						logger.Warn("UserInput node expired while waiting for signal", zap.String("NodeID", nodeID))
						status.State = shared.NodeStateExpired
						status.Error = fmt.Errorf("userInput node %s expired while waiting for signal", nodeID)
						// signalReceived remains false
					})
				}

				// Block until signal or expiry timer fires
				selectorForSignal.Select(ctx)


				if !signalReceived { // Means timer fired or channel closed
					if status.State != shared.NodeStateExpired { // If not already set by timer callback
						status.State = shared.NodeStateFailed // Default to failed if not expired (e.g. channel closed)
						if status.Error == nil {
							status.Error = fmt.Errorf("userInput node %s did not receive signal (channel closed or other issue)", nodeID)
						}
					}
					status.EndTime = workflow.Now(ctx).Unix()
					numCompletedOrSkippedNodes++
					numProcessingNodes--
					if errAggregator == nil {
						errAggregator = status.Error
					}
					// TODO: Propagate to dependents
					return // Exit this goroutine for the node
				}

				// Merge signalData into node.Params if signalData is a map
				if signalMap, ok := signalData.(map[string]interface{}); ok {
					if node.Params == nil {
						node.Params = make(map[string]interface{})
					}
					for k, v := range signalMap {
						node.Params[k] = v // Signal data can override original params
					}
					logger.Info("Merged signal data into node params", zap.String("NodeID", nodeID), zap.Any("NewParams", node.Params))
				}
			}
			// End UserInput Node Type specific logic

			status.State = shared.NodeStateRunning
			status.StartTime = workflow.Now(ctx).Unix()

			// --- Node Expiry Check (Original Expiry: before activity starts) ---
			// This expiry is about the node being too old to START its activity,
			// distinct from UserInput node expiry while waiting for signal.
			if node.Type != shared.NodeTypeUserInput && node.ExpirySeconds > 0 { // UserInput handles its own expiry while waiting
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
					nodeStatus := nodeStatuses[nodeID] // Get current status for attempt number

					// --- Result Validity Timer Setup (if node completed successfully) ---
					if nodeStatus.State == shared.NodeStateCompleted && node.ResultValiditySeconds > 0 {
						criticalDependentID, found := findCriticalDependent(nodeID, node, activityOutput)
						if found {
							depStatus, depOk := nodeStatuses[criticalDependentID]
							// Only set up timer if dependent is actually pending/running
							if depOk && (depStatus.State == shared.NodeStatePending || depStatus.State == shared.NodeStateRunning) {
								logger.Info("Node has ResultValiditySeconds, setting up timer.",
									zap.String("SourceNodeID", nodeID),
									zap.Int("ValiditySeconds", node.ResultValiditySeconds),
									zap.String("CriticalDependentID", criticalDependentID))

								timerCtx, cancelFunc := workflow.WithCancel(ctx)
								validityDuration := time.Duration(node.ResultValiditySeconds) * time.Second
								timerFuture := workflow.NewTimer(timerCtx, validityDuration)
								timerID := fmt.Sprintf("validity_%s_attempt_%d", nodeID, nodeStatus.Attempt)

								// Cancel and remove any pre-existing timer for the same source node (e.g., if source node was redone)
								for tid, existingTimer := range activeValidityTimers {
									if existingTimer.sourceNodeID == nodeID {
										logger.Debug("Cancelling old validity timer for redone source node", zap.String("OldTimerID", tid))
										existingTimer.timerCancelFunc()
										delete(activeValidityTimers, tid)
									}
								}

								activeValidityTimers[timerID] = &activeValidityTimerInfo{
									timerID:          timerID,
									sourceNodeID:     nodeID,
									dependentNodeID:  criticalDependentID,
									timerFuture:      timerFuture,
									timerCancelFunc:  cancelFunc,
								}

								// Add this new timer future to the main selector for handling its firing
								selector.AddFuture(timerFuture, func(f workflow.Future) {
									// Timer Fired! Result for sourceNodeID is now considered stale relative to its dependent.
									timerInfo, exists := activeValidityTimers[timerID]
									if !exists { // Timer might have been cancelled and entry removed by dependent completion
										logger.Info("ResultValidityTimer fired, but no active timer info found (timer was likely cancelled). Ignoring.", zap.String("TimerID", timerID))
										return
									}
									// Check if dependent completed *just* before this timer decision task started
									// This state check is crucial to avoid race conditions where dependent completes,
									// its completion handler tries to cancel timer, but this timer handler already got scheduled.
									dependentFinalStatusCheck := nodeStatuses[timerInfo.dependentNodeID]
									if dependentFinalStatusCheck.State == shared.NodeStateCompleted ||
										dependentFinalStatusCheck.State == shared.NodeStateSkipped ||
										dependentFinalStatusCheck.State == shared.NodeStateExpired ||
										dependentFinalStatusCheck.State == shared.NodeStateFailed { // Consider if Failed dependent also defuses timer
										logger.Info("ResultValidityTimer fired, but dependent node already reached a final state concurrently. No action needed.",
											zap.String("TimerID", timerID),
											zap.String("DependentNode", timerInfo.dependentNodeID),
											zap.String("DependentState", string(dependentFinalStatusCheck.State)))
										delete(activeValidityTimers, timerID) // Still clean up
										return
									}

									delete(activeValidityTimers, timerID) // Clean up before further processing

									logger.Warn("ResultValidityTimer fired. Source node result is stale. Resetting source and dependent.",
										zap.String("TimerID", timerID),
										zap.String("SourceNode", timerInfo.sourceNodeID),
										zap.String("DependentNode", timerInfo.dependentNodeID))

									// --- Reset Source Node (timerInfo.sourceNodeID) ---
									sourceStatusToReset := nodeStatuses[timerInfo.sourceNodeID]
									sourceNodeConfig := config.Nodes[timerInfo.sourceNodeID] // Assuming it exists

									logger.Info("Resetting source node due to result staleness.",
										zap.String("NodeID", timerInfo.sourceNodeID),
										zap.Int("OldAttempt", sourceStatusToReset.Attempt),
										zap.Int("NewAttempt", sourceStatusToReset.Attempt+1))

									sourceStatusToReset.State = shared.NodeStatePending
									sourceStatusToReset.Attempt++ // Increment attempt for the redo
									sourceStatusToReset.Error = nil
									sourceStatusToReset.Result = nil
									sourceStatusToReset.ScheduledTime = workflow.Now(ctx).Unix()
									sourceStatusToReset.StartTime = 0
									sourceStatusToReset.EndTime = 0

									// Decrement numCompletedOrSkippedNodes because this node is no longer considered "completed" for its previous attempt.
									// This is important if the workflow end condition relies on this counter.
									// Ensure this doesn't go negative if there are rapid resets.
									if numCompletedOrSkippedNodes > 0 { // Basic safety
										// numCompletedOrSkippedNodes-- // This might be too complex to manage correctly without deeper thought on finality.
										// Let's assume for now: a "reset" means the previous "completion" is voided for THIS path.
										// The overall workflow completion might need more robust checking than just this counter if resets are frequent.
										// For now, we focus on resetting state and re-queuing.
									}


									if sourceNodeConfig.Type == shared.NodeTypeUserInput {
										// For user_input, resetting to Pending is enough. It will await its signal again.
										logger.Info("Source node is UserInput, will re-await signal.", zap.String("NodeID", timerInfo.sourceNodeID))
									} else {
										// For other types, add back to readyQueue if not already there and all (original) dependencies are met.
										// Since it's being reset, its dependencies are effectively met again for the new attempt.
										// However, it might have its own explicit dependencies that need to be re-evaluated if the graph is very dynamic.
										// For simplicity: if it's not UserInput, put it back in readyQueue.
										// This assumes its original dependencies are still considered met for a redo.
										readyQueue <- timerInfo.sourceNodeID
									}

									// --- Reset Dependent Node (timerInfo.dependentNodeID) ---
									dependentStatusToReset := nodeStatuses[timerInfo.dependentNodeID]
									dependentNodeConfig := config.Nodes[timerInfo.dependentNodeID] // Assuming it exists

									// Only reset dependent if it's still in a state where it was waiting or could be affected.
									if dependentStatusToReset.State == shared.NodeStatePending || dependentStatusToReset.State == shared.NodeStateRunning {
										logger.Info("Resetting dependent node because its input source became stale.",
											zap.String("NodeID", timerInfo.dependentNodeID),
											zap.String("OldState", string(dependentStatusToReset.State)))

										dependentStatusToReset.State = shared.NodeStatePending
										dependentStatusToReset.Attempt = 1 // Reset attempts for dependent as it's a new context
										dependentStatusToReset.Error = nil
										dependentStatusToReset.Result = nil
										dependentStatusToReset.ScheduledTime = workflow.Now(ctx).Unix()
										dependentStatusToReset.StartTime = 0
										dependentStatusToReset.EndTime = 0


										if dependentNodeConfig.Type == shared.NodeTypeUserInput {
											logger.Info("Dependent node is UserInput, will re-await signal.", zap.String("NodeID", timerInfo.dependentNodeID))
											// If it was in the middle of an activity (not UserInput), that activity will be orphaned or might fail.
											// True cancellation of an in-flight activity from here is more complex (would require activity cancellation propagation).
											// The current model relies on the reset node re-evaluating its state when it runs next.
										} else {
											// If it's a regular node and was pending, it will re-evaluate its dependencies.
											// Since its source (timerInfo.sourceNodeID) is now also pending, it will wait.
											// No need to explicitly re-queue it here, as its dependency on sourceNodeID is not yet met.
										}
									} else {
										logger.Info("Dependent node was not in Pending/Running state when source result staled. No reset action for dependent.",
											zap.String("NodeID", timerInfo.dependentNodeID),
											zap.String("State", string(dependentStatusToReset.State)))
									}
								})
							} else {
								logger.Info("Critical dependent for validity timer not in a state to monitor or not found.",
									zap.String("SourceNodeID", nodeID),
									zap.String("DependentID", criticalDependentID))
							}
						}
					}
					// --- End Result Validity Timer Setup ---

					numCompletedOrSkippedNodes++ // Mark current node as completed for this attempt.

					// --- Check if this completed node was a tracked dependent, if so, cancel its source's validity timer ---
					for timerKey, timerInfo := range activeValidityTimers {
						if timerInfo.dependentNodeID == nodeID && nodeStatus.State == shared.NodeStateCompleted {
							logger.Info("Tracked dependent node completed. Cancelling validity timer for its source.",
								zap.String("DependentNodeID", nodeID),
								zap.String("SourceNodeToCancelTimerFor", timerInfo.sourceNodeID),
								zap.String("TimerKeyToCancel", timerKey))
							timerInfo.timerCancelFunc() // Call the cancel func
							delete(activeValidityTimers, timerKey) // Remove from map
						}
					}
					// --- End Check for Cancelling Timers ---

					// --- Conditional Path Selection (runs if node is marked complete) ---
					if nodeStatus.State == shared.NodeStateCompleted {
						if len(node.NextNodeRules) > 0 {
							logger.Info("Processing NextNodeRules for completed node", zap.String("NodeID", nodeID), zap.Int("numRules", len(node.NextNodeRules)))
						for _, ruleRef := range node.NextNodeRules { // ruleRef is of type shared.NextNodeRule
							actualRule, ruleExists := config.Rules[ruleRef.RuleID]
							if !ruleExists {
								logger.Error("RuleID from NextNodeRule not found in WorkflowConfig.Rules, skipping rule.",
									zap.String("NodeID", nodeID),
									zap.String("MissingRuleID", ruleRef.RuleID),
									zap.String("TargetNodeID", ruleRef.TargetNodeID))
								continue // Skip this rule definition
							}

							if evaluateCondition(ctx, actualRule.Expression, node.Params, activityOutput) {
								logger.Info("Condition met for rule, targeting next node.",
									zap.String("SourceNodeID", nodeID),
									zap.String("TargetNodeID", ruleRef.TargetNodeID),
									zap.String("RuleID", ruleRef.RuleID),
									zap.String("Expression", actualRule.Expression))

								if _, ok := dependencyCount[ruleRef.TargetNodeID]; ok {
									isDeclaredDependency := false
									if targetNodeConfig, okConfig := config.Nodes[ruleRef.TargetNodeID]; okConfig {
										for _, dep := range targetNodeConfig.Dependencies {
											if dep == nodeID {
												isDeclaredDependency = true
												break
											}
										}
									}

									if isDeclaredDependency {
										dependencyCount[ruleRef.TargetNodeID]--
										logger.Info("Dependency count for target node updated",
											zap.String("TargetNodeID", ruleRef.TargetNodeID),
											zap.Int("NewDepCount", dependencyCount[ruleRef.TargetNodeID]))

										if dependencyCount[ruleRef.TargetNodeID] == 0 && nodeStatuses[ruleRef.TargetNodeID].State == shared.NodeStatePending {
											logger.Info("All dependencies met for conditional target node, adding to ready queue", zap.String("NodeID", ruleRef.TargetNodeID))
											nodeStatuses[ruleRef.TargetNodeID].ScheduledTime = workflow.Now(ctx).Unix()
											readyQueue <- ruleRef.TargetNodeID
										}
									} else {
										// This rule targets a node that doesn't declare the current node as a direct dependency.
										// This implies a more dynamic graph where edges aren't all pre-declared.
										// For now, we'll only activate nodes that had this as a pre-declared dependency.
										// This could be a point of future enhancement for fully dynamic edges.
										logger.Warn("Conditional rule targets node that does not list current node as a static dependency. Ignoring for readiness.",
											zap.String("SourceNodeID", nodeID),
											zap.String("TargetNodeID", ruleRef.TargetNodeID))
									}
								} else {
									logger.Warn("Conditional rule targeted a node not in dependency count map", zap.String("TargetNodeID", ruleRef.TargetNodeID))
								}
							} else {
								logger.Debug("Condition NOT met for rule",
									zap.String("SourceNodeID", nodeID),
									zap.String("TargetNodeID", ruleRef.TargetNodeID),
									zap.String("RuleID", ruleRef.RuleID))
							}
						}
						} else {
						// Original behavior: propagate to all statically declared dependents if no rules.
						// This assumes dependentsMap is built from Node.Dependencies.
						logger.Info("No NextNodeRules, propagating to statically declared dependents", zap.String("NodeID", nodeID))
						for _, dependentID := range dependentsMap[nodeID] {
							dependencyCount[dependentID]-- // This node's part of dependency is met
							if dependencyCount[dependentID] == 0 && nodeStatuses[dependentID].State == shared.NodeStatePending {
								logger.Info("All dependencies met for static dependent node, adding to ready queue", zap.String("NodeID", dependentID))
								nodeStatuses[dependentID].ScheduledTime = workflow.Now(ctx).Unix()
								readyQueue <- dependentID
							}
						}
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
