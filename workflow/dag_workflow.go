package workflow

import (
	"flow-shift/shared"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

// DAGWorkflowInput is the input to the main DAG workflow
type DAGWorkflowInput struct {
	Config shared.WorkflowConfig
}

// DAGWorkflow executes a dynamic DAG based on the provided configuration
func DAGWorkflow(ctx workflow.Context, input DAGWorkflowInput) (map[string]interface{}, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("DAGWorkflow started", zap.Any("input", input))

	// Create and execute DAG engine
	engine := NewDAGEngine(ctx, input.Config)
	return engine.Execute()
}

// Legacy function name support for backward compatibility
func DAGWorkflowLegacy(ctx workflow.Context, input DAGWorkflowInput) (map[string]interface{}, error) {
	return DAGWorkflow(ctx, input)
}