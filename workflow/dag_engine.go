package workflow

import (
	"fmt"
	"sync"
	"time"

	"flow-shift/shared"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

// DAGEngine is the core engine responsible for executing dynamic DAGs
type DAGEngine struct {
	config         shared.WorkflowConfig
	nodeStatuses   map[string]*shared.NodeStatus
	nodeResults    map[string]interface{}
	dependencyMgr  *DependencyManager
	signalMgr      *SignalManager
	validityMgr    *ValidityManager
	nodeProcessor  *NodeProcessor
	conditionEval  *ConditionEvaluator
	logger         log.Logger
	ctx            workflow.Context
	selector       workflow.Selector
	readyQueue     chan string
	metrics        *DAGMetrics
	mu             sync.RWMutex
}

// DAGMetrics holds execution metrics
type DAGMetrics struct {
	TotalNodes              int
	CompletedOrSkippedNodes int
	ProcessingNodes         int
	FailedNodes             int
	SkippedNodes            int
	TimedOutNodes           int
	ExpiredNodes            int
}

// NewDAGEngine creates a new DAG execution engine
func NewDAGEngine(ctx workflow.Context, config shared.WorkflowConfig) *DAGEngine {
	logger := workflow.GetLogger(ctx)
	
	engine := &DAGEngine{
		config:       config,
		nodeStatuses: make(map[string]*shared.NodeStatus),
		nodeResults:  make(map[string]interface{}),
		logger:       logger,
		ctx:          ctx,
		selector:     workflow.NewSelector(ctx),
		readyQueue:   make(chan string, len(config.Nodes)),
		metrics: &DAGMetrics{
			TotalNodes: len(config.Nodes),
		},
	}

	// Initialize components
	engine.dependencyMgr = NewDependencyManager(config.Nodes, logger)
	engine.signalMgr = NewSignalManager(ctx, logger)
	engine.validityMgr = NewValidityManager(ctx, logger)
	engine.nodeProcessor = NewNodeProcessor(ctx, logger)
	engine.conditionEval = NewConditionEvaluator(ctx, logger)

	// Initialize node statuses
	engine.initializeNodeStatuses()

	return engine
}

// Execute runs the DAG workflow
func (e *DAGEngine) Execute() (map[string]interface{}, error) {
	e.logger.Info("DAG execution started", zap.Int("totalNodes", e.metrics.TotalNodes))
	
	// Initialize ready queue with start nodes
	if err := e.initializeReadyQueue(); err != nil {
		return nil, fmt.Errorf("failed to initialize ready queue: %w", err)
	}

	// Main execution loop
	var errAggregator error
	deadlockCounter := 0
	const maxDeadlockChecks = 5
	
	for !e.isExecutionComplete() {
		// Process ready nodes first
		nodeProcessed := false
		select {
		case nodeID := <-e.readyQueue:
			if err := e.processNode(nodeID); err != nil {
				e.logger.Error("Node processing failed", zap.String("nodeID", nodeID), zap.Error(err))
				if errAggregator == nil {
					errAggregator = err
				}
			}
			nodeProcessed = true
			deadlockCounter = 0 // Reset deadlock counter when we process nodes
		default:
			// No ready nodes to process
		}

		// If we have pending activities, wait for them to complete
		if e.metrics.ProcessingNodes > 0 {
			e.selector.Select(e.ctx)
			deadlockCounter = 0 // Reset deadlock counter when activities complete
		} else if !nodeProcessed {
			// No ready nodes and no processing nodes - potential deadlock
			deadlockCounter++
			if deadlockCounter >= maxDeadlockChecks && e.isDeadlocked() {
				e.logger.Error("DAG execution deadlocked", zap.Any("metrics", e.metrics))
				if errAggregator == nil {
					errAggregator = fmt.Errorf("DAG execution stalled with %d/%d nodes completed", 
						e.metrics.CompletedOrSkippedNodes, e.metrics.TotalNodes)
				}
				break
			}
			// Yield control to prevent tight loop
			workflow.Sleep(e.ctx, time.Millisecond*10)
		}
	}

	return e.getExecutionResults(errAggregator)
}

// initializeNodeStatuses sets up initial node statuses
func (e *DAGEngine) initializeNodeStatuses() {
	for id := range e.config.Nodes {
		e.nodeStatuses[id] = &shared.NodeStatus{
			State:   shared.NodeStatePending,
			Attempt: 1,
		}
	}
}

// initializeReadyQueue populates the ready queue with start nodes
func (e *DAGEngine) initializeReadyQueue() error {
	startNodes := e.dependencyMgr.GetReadyStartNodes(e.config.StartNodeIDs)
	for _, nodeID := range startNodes {
		e.scheduleNode(nodeID)
	}

	// Add orphaned nodes (nodes with no dependencies)
	orphanedNodes := e.dependencyMgr.GetOrphanedNodes(e.config.StartNodeIDs)
	for _, nodeID := range orphanedNodes {
		e.scheduleNode(nodeID)
	}

	if len(startNodes) == 0 && len(orphanedNodes) == 0 {
		return fmt.Errorf("no start nodes found or all start nodes have unmet dependencies")
	}

	return nil
}

// scheduleNode adds a node to the ready queue
func (e *DAGEngine) scheduleNode(nodeID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	
	if status, exists := e.nodeStatuses[nodeID]; exists {
		status.ScheduledTime = workflow.Now(e.ctx).Unix()
		e.readyQueue <- nodeID
		e.logger.Info("Node scheduled for processing", zap.String("nodeID", nodeID))
	}
}

// processNode handles the execution of a single node
func (e *DAGEngine) processNode(nodeID string) error {
	node, exists := e.config.Nodes[nodeID]
	if !exists {
		return fmt.Errorf("node %s not found in configuration", nodeID)
	}

	status := e.nodeStatuses[nodeID]
	e.logger.Info("Processing node", zap.String("nodeID", nodeID), zap.String("type", string(node.Type)))

	// Check if node should be skipped
	if e.shouldSkipNode(node, status) {
		return e.handleSkippedNode(nodeID)
	}

	// Handle user input nodes
	if node.Type == shared.NodeTypeUserInput {
		return e.handleUserInputNode(nodeID, node, status)
	}

	// Check node expiry
	if e.isNodeExpired(node, status) {
		return e.handleExpiredNode(nodeID, status)
	}

	// Execute the node activity
	return e.executeNodeActivity(nodeID, node, status)
}

// shouldSkipNode determines if a node should be skipped
func (e *DAGEngine) shouldSkipNode(node shared.Node, status *shared.NodeStatus) bool {
	if skipVal, ok := node.Params["_skip"].(bool); ok && skipVal {
		return true
	}
	
	// Additional skip condition evaluation can be added here
	if node.SkipCondition != "" {
		return e.conditionEval.EvaluateSkipCondition(node.SkipCondition, node.Params)
	}
	
	return false
}

// handleSkippedNode processes a skipped node
func (e *DAGEngine) handleSkippedNode(nodeID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	status := e.nodeStatuses[nodeID]
	status.State = shared.NodeStateSkipped
	status.EndTime = workflow.Now(e.ctx).Unix()
	e.nodeResults[nodeID] = "SKIPPED"
	e.metrics.CompletedOrSkippedNodes++
	e.metrics.SkippedNodes++

	e.logger.Info("Node skipped", zap.String("nodeID", nodeID))

	// Propagate completion to dependents
	return e.propagateToDependents(nodeID)
}

// handleUserInputNode processes a user input node
func (e *DAGEngine) handleUserInputNode(nodeID string, node shared.Node, status *shared.NodeStatus) error {
	if node.SignalName == "" {
		return fmt.Errorf("userInput node %s missing SignalName configuration", nodeID)
	}

	signalData, err := e.signalMgr.WaitForSignal(node.SignalName, node.ExpirySeconds)
	if err != nil {
		e.handleNodeError(nodeID, status, err)
		return err
	}

	// Merge signal data into node params
	if signalMap, ok := signalData.(map[string]interface{}); ok {
		if node.Params == nil {
			node.Params = make(map[string]interface{})
		}
		for k, v := range signalMap {
			node.Params[k] = v
		}
		e.logger.Info("Signal data merged into node params", zap.String("nodeID", nodeID))
	}

	// Now execute the activity with the signal data
	return e.executeNodeActivity(nodeID, node, status)
}

// isNodeExpired checks if a node has expired before execution
func (e *DAGEngine) isNodeExpired(node shared.Node, status *shared.NodeStatus) bool {
	if node.ExpirySeconds <= 0 {
		return false
	}

	expiryDuration := time.Duration(node.ExpirySeconds) * time.Second
	scheduledTime := time.Unix(status.ScheduledTime, 0)
	return workflow.Now(e.ctx).After(scheduledTime.Add(expiryDuration))
}

// handleExpiredNode processes an expired node
func (e *DAGEngine) handleExpiredNode(nodeID string, status *shared.NodeStatus) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	status.State = shared.NodeStateExpired
	status.EndTime = workflow.Now(e.ctx).Unix()
	status.Error = fmt.Errorf("node %s expired before execution", nodeID)
	e.nodeResults[nodeID] = nil
	e.metrics.CompletedOrSkippedNodes++
	e.metrics.ExpiredNodes++

	e.logger.Warn("Node expired before execution", zap.String("nodeID", nodeID))

	// Propagate to dependents
	return e.propagateToDependents(nodeID)
}

// executeNodeActivity executes the activity for a node
func (e *DAGEngine) executeNodeActivity(nodeID string, node shared.Node, status *shared.NodeStatus) error {
	e.mu.Lock()
	e.metrics.ProcessingNodes++
	e.mu.Unlock()

	status.State = shared.NodeStateRunning
	status.StartTime = workflow.Now(e.ctx).Unix()

	future := e.nodeProcessor.ExecuteActivity(node)
	
	e.selector.AddFuture(future, func(f workflow.Future) {
		e.handleActivityCompletion(nodeID, node, status, f)
	})

	return nil
}

// handleActivityCompletion processes the result of an activity execution
func (e *DAGEngine) handleActivityCompletion(nodeID string, node shared.Node, status *shared.NodeStatus, future workflow.Future) {
	e.mu.Lock()
	e.metrics.ProcessingNodes--
	e.mu.Unlock()

	var activityOutput interface{}
	err := future.Get(e.ctx, &activityOutput)
	
	e.mu.Lock()
	status.EndTime = workflow.Now(e.ctx).Unix()
	e.mu.Unlock()

	if err != nil {
		e.handleActivityError(nodeID, node, status, err)
		return
	}

	// Handle successful completion
	e.handleActivitySuccess(nodeID, node, status, activityOutput)
}

// handleActivityError processes activity execution errors
func (e *DAGEngine) handleActivityError(nodeID string, node shared.Node, status *shared.NodeStatus, err error) {
	e.logger.Error("Activity execution failed", zap.String("nodeID", nodeID), zap.Error(err))

	// Determine error type and set appropriate state
	if e.nodeProcessor.IsTimeoutError(err) {
		status.State = shared.NodeStateTimedOut
		e.metrics.TimedOutNodes++
	} else {
		status.State = shared.NodeStateFailed
		e.metrics.FailedNodes++
	}
	
	status.Error = err

	// Check if redo is possible
	if e.shouldRedoNode(node, status) {
		e.handleNodeRedo(nodeID, status)
		return
	}

	// Mark as completed and propagate
	e.metrics.CompletedOrSkippedNodes++
	e.propagateToDependents(nodeID)
}

// handleActivitySuccess processes successful activity completion
func (e *DAGEngine) handleActivitySuccess(nodeID string, node shared.Node, status *shared.NodeStatus, activityOutput interface{}) {
	e.logger.Info("Activity execution completed", zap.String("nodeID", nodeID))
	
	status.State = shared.NodeStateCompleted
	status.Result = activityOutput
	e.nodeResults[nodeID] = activityOutput
	e.metrics.CompletedOrSkippedNodes++

	// Set up result validity timer if needed
	if node.ResultValiditySeconds > 0 {
		e.validityMgr.SetupValidityTimer(nodeID, node, status, activityOutput)
	}

	// Process conditional routing
	e.processConditionalRouting(nodeID, node, activityOutput)

	// Propagate to static dependents if no conditional rules
	if len(node.NextNodeRules) == 0 {
		e.propagateToDependents(nodeID)
	}
}

// shouldRedoNode determines if a failed node should be retried
func (e *DAGEngine) shouldRedoNode(node shared.Node, status *shared.NodeStatus) bool {
	maxAttempts := 3 // Could be configurable
	return status.State == shared.NodeStateFailed &&
		node.RedoCondition == "on_failure" &&
		status.Attempt < maxAttempts
}

// handleNodeRedo handles retrying a failed node
func (e *DAGEngine) handleNodeRedo(nodeID string, status *shared.NodeStatus) {
	e.logger.Info("Retrying failed node", zap.String("nodeID", nodeID), zap.Int("attempt", status.Attempt))
	
	status.Attempt++
	status.State = shared.NodeStatePending
	status.Error = nil
	status.ScheduledTime = workflow.Now(e.ctx).Unix()
	
	// Re-schedule the node
	e.readyQueue <- nodeID
}

// processConditionalRouting handles conditional routing based on NextNodeRules
func (e *DAGEngine) processConditionalRouting(nodeID string, node shared.Node, activityOutput interface{}) {
	if len(node.NextNodeRules) == 0 {
		return
	}

	e.logger.Info("Processing conditional routing", zap.String("nodeID", nodeID), zap.Int("numRules", len(node.NextNodeRules)))

	for _, ruleRef := range node.NextNodeRules {
		rule, exists := e.config.Rules[ruleRef.RuleID]
		if !exists {
			e.logger.Error("Rule not found", zap.String("ruleID", ruleRef.RuleID))
			continue
		}

		if e.conditionEval.EvaluateCondition(rule.Expression, node.Params, activityOutput) {
			e.logger.Info("Condition met for rule", zap.String("ruleID", ruleRef.RuleID), zap.String("targetNode", ruleRef.TargetNodeID))
			e.dependencyMgr.DecrementDependency(ruleRef.TargetNodeID, nodeID, e.nodeStatuses, e.readyQueue, e.ctx)
		}
	}
}

// propagateToDependents notifies dependent nodes of completion
func (e *DAGEngine) propagateToDependents(nodeID string) error {
	dependents := e.dependencyMgr.GetDependents(nodeID)
	for _, dependentID := range dependents {
		e.dependencyMgr.DecrementDependency(dependentID, nodeID, e.nodeStatuses, e.readyQueue, e.ctx)
	}
	return nil
}

// handleNodeError handles general node errors
func (e *DAGEngine) handleNodeError(nodeID string, status *shared.NodeStatus, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	status.State = shared.NodeStateFailed
	status.Error = err
	status.EndTime = workflow.Now(e.ctx).Unix()
	e.metrics.CompletedOrSkippedNodes++
	e.metrics.FailedNodes++

	e.logger.Error("Node failed", zap.String("nodeID", nodeID), zap.Error(err))
}

// isExecutionComplete checks if the DAG execution is complete
func (e *DAGEngine) isExecutionComplete() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.metrics.CompletedOrSkippedNodes >= e.metrics.TotalNodes
}

// isDeadlocked checks if the execution is deadlocked
func (e *DAGEngine) isDeadlocked() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.metrics.CompletedOrSkippedNodes < e.metrics.TotalNodes &&
		len(e.readyQueue) == 0 &&
		e.metrics.ProcessingNodes == 0
}

// getExecutionResults returns the final execution results
func (e *DAGEngine) getExecutionResults(errAggregator error) (map[string]interface{}, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.metrics.CompletedOrSkippedNodes < e.metrics.TotalNodes && errAggregator == nil {
		e.logger.Warn("Workflow finished but not all nodes completed", zap.Any("metrics", e.metrics))
		errAggregator = fmt.Errorf("workflow incomplete: %d/%d nodes processed", 
			e.metrics.CompletedOrSkippedNodes, e.metrics.TotalNodes)
	}

	if errAggregator != nil {
		e.logger.Error("DAG execution finished with errors", zap.Error(errAggregator), zap.Any("metrics", e.metrics))
	} else {
		e.logger.Info("DAG execution finished successfully", zap.Any("metrics", e.metrics))
	}

	// Return a copy of results to prevent external modification
	results := make(map[string]interface{})
	for k, v := range e.nodeResults {
		results[k] = v
	}

	return results, errAggregator
}