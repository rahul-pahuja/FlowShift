package workflow

import (
	"errors"
	"time"

	"flow-shift/shared"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// NodeProcessor handles the execution of node activities
type NodeProcessor struct {
	ctx    workflow.Context
	logger log.Logger
}

// NewNodeProcessor creates a new node processor
func NewNodeProcessor(ctx workflow.Context, logger log.Logger) *NodeProcessor {
	return &NodeProcessor{
		ctx:    ctx,
		logger: logger,
	}
}

// ExecuteActivity executes an activity for a given node
func (np *NodeProcessor) ExecuteActivity(node shared.Node) workflow.Future {
	activityOptions := np.buildActivityOptions(node)
	activityCtx := workflow.WithActivityOptions(np.ctx, activityOptions)

	np.logger.Info("Executing activity",
		"nodeID", node.ID,
		"activityName", node.ActivityName,
		"params", node.Params,
		"timeout", activityOptions.StartToCloseTimeout)

	return workflow.ExecuteActivity(activityCtx, node.ActivityName, node.Params)
}

// buildActivityOptions constructs activity options based on node configuration
func (np *NodeProcessor) buildActivityOptions(node shared.Node) workflow.ActivityOptions {
	options := workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Minute, // Default timeout
		HeartbeatTimeout:    0,                 // No heartbeat by default
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1, // No retries by default (handled by node-level redo logic)
		},
	}

	// Set custom timeout if specified
	if node.ActivityTimeoutSeconds > 0 {
		options.StartToCloseTimeout = time.Duration(node.ActivityTimeoutSeconds) * time.Second
	}

	// Configure heartbeat for long-running activities
	if np.shouldUseHeartbeat(node) {
		options.HeartbeatTimeout = np.calculateHeartbeatTimeout(node)
		np.logger.Info("Heartbeat configured for activity",
			"nodeID", node.ID,
			"heartbeatTimeout", options.HeartbeatTimeout)
	}

	// Configure retry policy based on node configuration
	if node.RedoCondition != "" {
		options.RetryPolicy = np.buildRetryPolicy(node)
	}

	return options
}

// shouldUseHeartbeat determines if an activity should use heartbeating
func (np *NodeProcessor) shouldUseHeartbeat(node shared.Node) bool {
	// Use heartbeat for wait activities or long-running activities
	return node.Type == shared.NodeTypeWait ||
		node.ActivityName == "WaitActivityExample" ||
		node.ActivityTimeoutSeconds > 300 // Activities longer than 5 minutes
}

// calculateHeartbeatTimeout calculates appropriate heartbeat timeout
func (np *NodeProcessor) calculateHeartbeatTimeout(node shared.Node) time.Duration {
	if node.Type == shared.NodeTypeWait {
		// For wait activities, heartbeat should be frequent
		return 20 * time.Second
	}

	// For other long-running activities, use a fraction of the total timeout
	if node.ActivityTimeoutSeconds > 0 {
		timeout := time.Duration(node.ActivityTimeoutSeconds) * time.Second
		return timeout / 4 // Heartbeat every quarter of the total timeout
	}

	return 30 * time.Second // Default heartbeat interval
}

// buildRetryPolicy constructs a retry policy based on node configuration
func (np *NodeProcessor) buildRetryPolicy(node shared.Node) *temporal.RetryPolicy {
	policy := &temporal.RetryPolicy{
		MaximumAttempts: 1, // Default: no automatic retries
	}

	// Customize based on redo condition
	switch node.RedoCondition {
	case "on_failure":
		// Allow some retries for transient failures
		policy.MaximumAttempts = 2
		policy.InitialInterval = 1 * time.Second
		policy.BackoffCoefficient = 2.0
		policy.MaximumInterval = 30 * time.Second
	case "always":
		// More aggressive retry for critical nodes
		policy.MaximumAttempts = 3
		policy.InitialInterval = 500 * time.Millisecond
		policy.BackoffCoefficient = 1.5
		policy.MaximumInterval = 15 * time.Second
	}

	return policy
}

// IsTimeoutError checks if an error is a timeout error
func (np *NodeProcessor) IsTimeoutError(err error) bool {
	var timeoutErr *temporal.TimeoutError
	return temporal.IsTimeoutError(err) && 
		errors.As(err, &timeoutErr) && 
		timeoutErr.TimeoutType() == enumspb.TIMEOUT_TYPE_START_TO_CLOSE
}

// IsRetryableError checks if an error is retryable
func (np *NodeProcessor) IsRetryableError(err error) bool {
	// Timeout errors are generally not retryable at the node level
	if np.IsTimeoutError(err) {
		return false
	}

	// Application errors might be retryable depending on the cause
	var appErr *temporal.ApplicationError
	if temporal.IsApplicationError(err) && errors.As(err, &appErr) {
		// Check if the application error is marked as non-retryable
		return appErr.Type() != "NonRetryableError"
	}

	// Other errors are generally retryable
	return true
}

// GetErrorType returns a string representation of the error type
func (np *NodeProcessor) GetErrorType(err error) string {
	if np.IsTimeoutError(err) {
		return "timeout"
	}

	var appErr *temporal.ApplicationError
	if temporal.IsApplicationError(err) && errors.As(err, &appErr) {
		return "application_error"
	}

	// Check for other specific error types as needed
	// var activityErr *temporal.ActivityError
	// if temporal.IsActivityError(err) && errors.As(err, &activityErr) {
	//     return "activity_error"
	// }

	return "unknown_error"
}

// LogActivityExecution logs details about activity execution
func (np *NodeProcessor) LogActivityExecution(node shared.Node, result interface{}, err error, duration time.Duration) {
	keyvals := []interface{}{
		"nodeID", node.ID,
		"activityName", node.ActivityName,
		"duration", duration,
	}

	if err != nil {
		keyvals = append(keyvals,
			"error", err.Error(),
			"errorType", np.GetErrorType(err),
			"retryable", np.IsRetryableError(err))
		np.logger.Error("Activity execution failed", keyvals...)
	} else {
		keyvals = append(keyvals, "result", result)
		np.logger.Info("Activity execution completed successfully", keyvals...)
	}
}