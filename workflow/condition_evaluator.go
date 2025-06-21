package workflow

import (
	"fmt"
	"strings"

	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

// evaluateCondition evaluates a simple condition string against node parameters and activity output.
// Format for conditionStr:
// - "true" or empty: always true
// - "output.KEY == 'VALUE'": checks activityOutput (if map) for KEY matching VALUE (string only for now)
// - "output == 'VALUE'": checks activityOutput (if string) directly against VALUE
// - "params.KEY == 'VALUE'": checks nodeParams for KEY matching VALUE (string only for now)
// This is a very basic evaluator and can be replaced with a proper expression language engine.
func evaluateCondition(ctx workflow.Context, conditionStr string, nodeParams map[string]interface{}, activityOutput interface{}) bool {
	logger := workflow.GetLogger(ctx)
	conditionStr = strings.TrimSpace(conditionStr)

	if conditionStr == "" || conditionStr == "true" {
		return true
	}

	parts := strings.Split(conditionStr, "==")
	if len(parts) != 2 {
		logger.Warn("Unsupported condition format (must contain '==')", zap.String("condition", conditionStr))
		return false
	}

	lhs := strings.TrimSpace(parts[0])
	rhs := strings.TrimSpace(parts[1])

	// Remove surrounding quotes from rhs if present
	if (strings.HasPrefix(rhs, "'") && strings.HasSuffix(rhs, "'")) ||
		(strings.HasPrefix(rhs, "\"") && strings.HasSuffix(rhs, "\"")) {
		rhs = rhs[1 : len(rhs)-1]
	}

	var valueToCompare interface{}
	found := false

	if strings.HasPrefix(lhs, "output.") {
		key := strings.TrimPrefix(lhs, "output.")
		if outputMap, ok := activityOutput.(map[string]interface{}); ok {
			valueToCompare, found = outputMap[key]
		} else {
			logger.Debug("Activity output is not a map, cannot evaluate output.KEY", zap.String("condition", conditionStr), zap.Any("outputType", fmt.Sprintf("%T", activityOutput)))
			return false
		}
	} else if lhs == "output" {
		valueToCompare = activityOutput
		found = true // output itself is the value
	} else if strings.HasPrefix(lhs, "params.") {
		key := strings.TrimPrefix(lhs, "params.")
		valueToCompare, found = nodeParams[key]
	} else {
		logger.Warn("Unsupported LHS in condition (must start with 'output.' or 'params.' or be 'output')", zap.String("lhs", lhs), zap.String("condition", conditionStr))
		return false
	}

	if !found {
		logger.Debug("LHS key not found for condition", zap.String("lhs", lhs), zap.String("condition", conditionStr))
		return false
	}

	// For now, only string comparison is robustly supported for simplicity.
	// Other types might work if they stringify well or are directly comparable.
	valueStr, ok := valueToCompare.(string)
	if !ok {
		// Attempt to convert to string if not already, for basic comparison
		// This is a simplification; a real expression engine would handle types.
		valueStr = fmt.Sprintf("%v", valueToCompare)
		logger.Debug("LHS value was not a string, converted for comparison", zap.String("lhs", lhs), zap.Any("originalValue", valueToCompare), zap.String("convertedValue", valueStr))
		// return false // Stricter: if not a string, don't compare unless RHS is also not string-like
	}


	isEqual := valueStr == rhs
	logger.Info("Evaluated condition",
		zap.String("condition", conditionStr),
		zap.String("lhs", lhs),
		zap.String("rhs", rhs),
		zap.Any("valueToCompare", valueToCompare),
		zap.Bool("result", isEqual))
	return isEqual
}
