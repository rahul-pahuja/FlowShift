package workflow

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

// ConditionEvaluator handles evaluation of conditions for rules and skip logic
type ConditionEvaluator struct {
	ctx    workflow.Context
	logger log.Logger
}

// NewConditionEvaluator creates a new condition evaluator
func NewConditionEvaluator(ctx workflow.Context, logger log.Logger) *ConditionEvaluator {
	return &ConditionEvaluator{
		ctx:    ctx,
		logger: logger,
	}
}

// EvaluateCondition evaluates a condition expression against node parameters and activity output
func (ce *ConditionEvaluator) EvaluateCondition(expression string, nodeParams map[string]interface{}, activityOutput interface{}) bool {
	ce.logger.Debug("Evaluating condition", zap.String("expression", expression))
	
	result := ce.evaluateExpression(expression, nodeParams, activityOutput)
	
	ce.logger.Info("Condition evaluated",
		zap.String("expression", expression),
		zap.Bool("result", result))
	
	return result
}

// EvaluateSkipCondition evaluates a skip condition for a node
func (ce *ConditionEvaluator) EvaluateSkipCondition(condition string, nodeParams map[string]interface{}) bool {
	ce.logger.Debug("Evaluating skip condition", zap.String("condition", condition))
	
	// Skip conditions only have access to node parameters, not activity output
	result := ce.evaluateExpression(condition, nodeParams, nil)
	
	ce.logger.Info("Skip condition evaluated",
		zap.String("condition", condition),
		zap.Bool("result", result))
	
	return result
}

// evaluateExpression is the core expression evaluation logic
func (ce *ConditionEvaluator) evaluateExpression(expression string, nodeParams map[string]interface{}, activityOutput interface{}) bool {
	expression = strings.TrimSpace(expression)

	// Handle simple boolean expressions
	if expression == "" || expression == "true" {
		return true
	}
	if expression == "false" {
		return false
	}

	// Handle comparison expressions
	if strings.Contains(expression, "==") {
		return ce.evaluateEquality(expression, nodeParams, activityOutput)
	}
	if strings.Contains(expression, "!=") {
		return !ce.evaluateEquality(strings.ReplaceAll(expression, "!=", "=="), nodeParams, activityOutput)
	}
	if strings.Contains(expression, ">") {
		return ce.evaluateComparison(expression, nodeParams, activityOutput, ">")
	}
	if strings.Contains(expression, "<") {
		return ce.evaluateComparison(expression, nodeParams, activityOutput, "<")
	}
	if strings.Contains(expression, ">=") {
		return ce.evaluateComparison(expression, nodeParams, activityOutput, ">=")
	}
	if strings.Contains(expression, "<=") {
		return ce.evaluateComparison(expression, nodeParams, activityOutput, "<=")
	}

	// Handle logical expressions
	if strings.Contains(expression, "&&") {
		return ce.evaluateLogicalAnd(expression, nodeParams, activityOutput)
	}
	if strings.Contains(expression, "||") {
		return ce.evaluateLogicalOr(expression, nodeParams, activityOutput)
	}

	// Handle function calls
	if strings.Contains(expression, "(") && strings.Contains(expression, ")") {
		return ce.evaluateFunction(expression, nodeParams, activityOutput)
	}

	ce.logger.Warn("Unsupported expression format", zap.String("expression", expression))
	return false
}

// evaluateEquality handles equality comparisons
func (ce *ConditionEvaluator) evaluateEquality(expression string, nodeParams map[string]interface{}, activityOutput interface{}) bool {
	parts := strings.Split(expression, "==")
	if len(parts) != 2 {
		ce.logger.Warn("Invalid equality expression", zap.String("expression", expression))
		return false
	}

	lhs := strings.TrimSpace(parts[0])
	rhs := strings.TrimSpace(parts[1])

	leftValue := ce.resolveValue(lhs, nodeParams, activityOutput)
	rightValue := ce.resolveValue(rhs, nodeParams, activityOutput)

	return ce.compareValues(leftValue, rightValue)
}

// evaluateComparison handles numerical comparisons
func (ce *ConditionEvaluator) evaluateComparison(expression string, nodeParams map[string]interface{}, activityOutput interface{}, operator string) bool {
	parts := strings.Split(expression, operator)
	if len(parts) != 2 {
		ce.logger.Warn("Invalid comparison expression", zap.String("expression", expression))
		return false
	}

	lhs := strings.TrimSpace(parts[0])
	rhs := strings.TrimSpace(parts[1])

	leftValue := ce.resolveValue(lhs, nodeParams, activityOutput)
	rightValue := ce.resolveValue(rhs, nodeParams, activityOutput)

	return ce.compareNumerically(leftValue, rightValue, operator)
}

// evaluateLogicalAnd handles logical AND operations
func (ce *ConditionEvaluator) evaluateLogicalAnd(expression string, nodeParams map[string]interface{}, activityOutput interface{}) bool {
	parts := strings.Split(expression, "&&")
	for _, part := range parts {
		if !ce.evaluateExpression(strings.TrimSpace(part), nodeParams, activityOutput) {
			return false
		}
	}
	return true
}

// evaluateLogicalOr handles logical OR operations
func (ce *ConditionEvaluator) evaluateLogicalOr(expression string, nodeParams map[string]interface{}, activityOutput interface{}) bool {
	parts := strings.Split(expression, "||")
	for _, part := range parts {
		if ce.evaluateExpression(strings.TrimSpace(part), nodeParams, activityOutput) {
			return true
		}
	}
	return false
}

// evaluateFunction handles function calls in expressions
func (ce *ConditionEvaluator) evaluateFunction(expression string, nodeParams map[string]interface{}, activityOutput interface{}) bool {
	// Simple function parsing - can be extended for more complex functions
	if strings.HasPrefix(expression, "exists(") && strings.HasSuffix(expression, ")") {
		arg := strings.TrimSpace(expression[7 : len(expression)-1])
		value := ce.resolveValue(arg, nodeParams, activityOutput)
		return value != nil
	}

	if strings.HasPrefix(expression, "isEmpty(") && strings.HasSuffix(expression, ")") {
		arg := strings.TrimSpace(expression[8 : len(expression)-1])
		value := ce.resolveValue(arg, nodeParams, activityOutput)
		return ce.isEmpty(value)
	}

	if strings.HasPrefix(expression, "isType(") && strings.HasSuffix(expression, ")") {
		// isType(value, "string") - checks if value is of specified type
		args := strings.Split(expression[7:len(expression)-1], ",")
		if len(args) == 2 {
			value := ce.resolveValue(strings.TrimSpace(args[0]), nodeParams, activityOutput)
			expectedType := strings.Trim(strings.TrimSpace(args[1]), "\"'")
			return ce.isType(value, expectedType)
		}
	}

	ce.logger.Warn("Unsupported function in expression", zap.String("expression", expression))
	return false
}

// resolveValue resolves a value from an expression token
func (ce *ConditionEvaluator) resolveValue(token string, nodeParams map[string]interface{}, activityOutput interface{}) interface{} {
	token = strings.TrimSpace(token)

	// Remove quotes if present
	if (strings.HasPrefix(token, "'") && strings.HasSuffix(token, "'")) ||
		(strings.HasPrefix(token, "\"") && strings.HasSuffix(token, "\"")) {
		return token[1 : len(token)-1]
	}

	// Handle numeric literals
	if num, err := strconv.ParseFloat(token, 64); err == nil {
		return num
	}

	// Handle boolean literals
	if token == "true" {
		return true
	}
	if token == "false" {
		return false
	}

	// Handle null/nil
	if token == "null" || token == "nil" {
		return nil
	}

	// Handle output references
	if strings.HasPrefix(token, "output.") {
		key := strings.TrimPrefix(token, "output.")
		return ce.getNestedValue(activityOutput, key)
	}
	if token == "output" {
		return activityOutput
	}

	// Handle parameter references
	if strings.HasPrefix(token, "params.") {
		key := strings.TrimPrefix(token, "params.")
		return ce.getNestedValue(nodeParams, key)
	}

	// If no prefix, treat as literal string
	return token
}

// getNestedValue gets a value from a nested structure using dot notation
func (ce *ConditionEvaluator) getNestedValue(data interface{}, path string) interface{} {
	if data == nil {
		return nil
	}

	keys := strings.Split(path, ".")
	current := data

	for _, key := range keys {
		switch v := current.(type) {
		case map[string]interface{}:
			current = v[key]
		case map[interface{}]interface{}:
			current = v[key]
		default:
			// Try to use reflection for struct fields
			if current = ce.getStructField(current, key); current == nil {
				return nil
			}
		}
	}

	return current
}

// getStructField gets a field from a struct using reflection
func (ce *ConditionEvaluator) getStructField(obj interface{}, fieldName string) interface{} {
	if obj == nil {
		return nil
	}

	val := reflect.ValueOf(obj)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct {
		return nil
	}

	field := val.FieldByName(fieldName)
	if !field.IsValid() {
		return nil
	}

	return field.Interface()
}

// compareValues compares two values for equality
func (ce *ConditionEvaluator) compareValues(left, right interface{}) bool {
	// Handle nil comparisons
	if left == nil && right == nil {
		return true
	}
	if left == nil || right == nil {
		return false
	}

	// Convert both to strings for comparison if different types
	leftStr := fmt.Sprintf("%v", left)
	rightStr := fmt.Sprintf("%v", right)

	return leftStr == rightStr
}

// compareNumerically compares two values numerically
func (ce *ConditionEvaluator) compareNumerically(left, right interface{}, operator string) bool {
	leftNum, leftOk := ce.toFloat64(left)
	rightNum, rightOk := ce.toFloat64(right)

	if !leftOk || !rightOk {
		ce.logger.Warn("Non-numeric values in numeric comparison",
			zap.Any("left", left),
			zap.Any("right", right))
		return false
	}

	switch operator {
	case ">":
		return leftNum > rightNum
	case "<":
		return leftNum < rightNum
	case ">=":
		return leftNum >= rightNum
	case "<=":
		return leftNum <= rightNum
	default:
		return false
	}
}

// toFloat64 converts a value to float64 if possible
func (ce *ConditionEvaluator) toFloat64(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		if num, err := strconv.ParseFloat(v, 64); err == nil {
			return num, true
		}
	}
	return 0, false
}

// isEmpty checks if a value is empty
func (ce *ConditionEvaluator) isEmpty(value interface{}) bool {
	if value == nil {
		return true
	}

	switch v := value.(type) {
	case string:
		return v == ""
	case []interface{}:
		return len(v) == 0
	case map[string]interface{}:
		return len(v) == 0
	default:
		val := reflect.ValueOf(value)
		switch val.Kind() {
		case reflect.Array, reflect.Slice, reflect.Map, reflect.Chan:
			return val.Len() == 0
		case reflect.Ptr, reflect.Interface:
			return val.IsNil()
		}
	}

	return false
}

// isType checks if a value is of a specific type
func (ce *ConditionEvaluator) isType(value interface{}, expectedType string) bool {
	if value == nil {
		return expectedType == "nil" || expectedType == "null"
	}

	actualType := reflect.TypeOf(value).String()
	switch expectedType {
	case "string":
		_, ok := value.(string)
		return ok
	case "number", "float":
		_, ok := ce.toFloat64(value)
		return ok
	case "bool", "boolean":
		_, ok := value.(bool)
		return ok
	case "array", "slice":
		return strings.Contains(actualType, "[]")
	case "map", "object":
		return strings.Contains(actualType, "map[")
	default:
		return actualType == expectedType
	}
}
