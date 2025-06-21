package activities

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"
	"go.uber.org/zap"
)

// SimpleSyncActivity is a basic synchronous activity.
func SimpleSyncActivity(ctx context.Context, params map[string]interface{}) (string, error) {
	logger := activity.GetLogger(ctx)
	activityName := "SimpleSyncActivity" // Or get from activity.GetInfo(ctx).ActivityType.Name
	logger.Info("Executing SimpleSyncActivity", zap.Any("params", params))

	// Simulate some work
	time.Sleep(2 * time.Second)

	output := fmt.Sprintf("Output from %s with params: %v", activityName, params)
	logger.Info("SimpleSyncActivity completed")
	return output, nil
}

// AsyncActivityExample simulates an activity that might complete asynchronously
// For Temporal, "asynchronous completion" means the activity function returns
// before the logical work is done, and another process completes it later using a task token.
// This example will just be a synchronous execution for now, as true async completion
// requires external interaction.
func AsyncActivityExample(ctx context.Context, params map[string]interface{}) (string, error) {
	logger := activity.GetLogger(ctx)
	activityName := "AsyncActivityExample"
	logger.Info("Executing AsyncActivityExample", zap.Any("params", params))

	// Simulate some work
	time.Sleep(3 * time.Second)

	output := fmt.Sprintf("Output from %s with params: %v", activityName, params)
	logger.Info("AsyncActivityExample completed (simulated)")
	return output, nil
}

// WaitActivityExample simulates an activity that waits for a condition or time.
// In a real scenario, this might involve polling or waiting for an external signal.
func WaitActivityExample(ctx context.Context, params map[string]interface{}) (string, error) {
	logger := activity.GetLogger(ctx)
	activityName := "WaitActivityExample"
	logger.Info("Executing WaitActivityExample", zap.Any("params", params), zap.Duration("waitTime", 5*time.Second))

	// Simulate waiting
	// For long waits, heartbeating is crucial:
	// go func() {
	// 	for {
	// 		select {
	// 		case <-time.After(10 * time.Second): // Heartbeat interval
	// 			activity.RecordHeartbeat(ctx, "still waiting")
	// 		case <-ctx.Done(): // Context cancellation
	// 			return
	// 		}
	// 	}
	// }()

	heartbeatInterval := 2 * time.Second
	waitDuration := 5 * time.Second
	startTime := time.Now()

	for time.Since(startTime) < waitDuration {
		select {
		case <-time.After(heartbeatInterval):
			activity.RecordHeartbeat(ctx, "Still waiting...", time.Since(startTime))
			logger.Info("WaitActivityExample heartbeat", zap.Duration("elapsed", time.Since(startTime)))
		case <-ctx.Done():
			logger.Warn("WaitActivityExample context cancelled", zap.Error(ctx.Err()))
			return "", ctx.Err()
		}
		// Check if overall activity context is done (e.g. timeout)
		if ctx.Err() != nil {
			logger.Warn("WaitActivityExample activity context ended", zap.Error(ctx.Err()))
			return "", ctx.Err()
		}
	}


	output := fmt.Sprintf("Output from %s after waiting, params: %v", activityName, params)
	logger.Info("WaitActivityExample completed")
	return output, nil
}

// ActivityThatCanFail demonstrates an activity that might fail, for testing redo logic.
func ActivityThatCanFail(ctx context.Context, params map[string]interface{}) (string, error) {
	logger := activity.GetLogger(ctx)
	activityName := "ActivityThatCanFail"
	logger.Info("Executing ActivityThatCanFail", zap.Any("params", params))

	fail := false
	if val, ok := params["forceFail"].(bool); ok {
		fail = val
	}

	attempt := activity.GetInfo(ctx).Attempt
	logger.Info("ActivityAttempt", zap.Int32("attempt", attempt))

	if fail && attempt < 3 { // Fail for the first 2 attempts if forceFail is true
		logger.Warn("ActivityThatCanFail is failing (simulated)", zap.Int32("attempt", attempt))
		return "", fmt.Errorf("simulated failure for %s on attempt %d", activityName, attempt)
	}

	output := fmt.Sprintf("Output from %s (attempt %d), params: %v", activityName, attempt, params)
	logger.Info("ActivityThatCanFail completed successfully")
	return output, nil
}
