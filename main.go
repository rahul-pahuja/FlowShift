package main

import (
	"context"
	"flow-shift/activities"
	"flow-shift/workflow"
	"log"
	"os"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const TaskQueueName = "dynamic-dag-task-queue"

func main() {
	// Configure structured logging
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	logger, err := config.Build()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Sync() // flushes buffer, if any

	// Create Temporal client
	temporalClient, err := client.Dial(client.Options{
		HostPort: client.DefaultHostPort, // Connects to localhost:7233 or TEMPORAL_GRPC_ENDPOINT
		Logger:   logger,                 // Use our structured logger
	})
	if err != nil {
		logger.Fatal("Unable to create Temporal client", zap.Error(err))
	}
	defer temporalClient.Close()

	// Check if running as worker, starter, or both (default)
	runMode := os.Getenv("RUN_MODE") // e.g., WORKER, STARTER

	if runMode == "WORKER" || runMode == "" {
		startWorker(temporalClient, logger) // Call to a function that blocks
	}

	if runMode == "STARTER" || runMode == "" {
		// Give worker a moment to start up if running in combined mode
		if runMode == "" {
			logger.Info("Running in combined mode, waiting for worker to potentially start...")
			time.Sleep(2 * time.Second)
		}
		startSampleWorkflow(temporalClient, logger)
		if runMode == "STARTER" { // if only starter, exit after starting
			return
		}
	}

	// If only worker, startWorker would have blocked. If combined, keep running for worker.
    // If we reach here in combined mode, it means startWorker has returned, which it shouldn't for a long-running worker.
    // For simplicity, if combined mode, we rely on startWorker blocking indefinitely.
    // If startWorker is designed to be non-blocking or to run in a goroutine,
    // then a select{} or similar mechanism would be needed here to keep main alive.
    // For this setup, startWorker will be blocking.
    if runMode == "" {
        select{} // Keep main alive for the worker in combined mode
    }
}

func startWorker(c client.Client, logger *zap.Logger) {
	logger.Info("Starting Worker...", zap.String("TaskQueue", TaskQueueName))
	w := worker.New(c, TaskQueueName, worker.Options{
		// Configure worker options if needed:
		// MaxConcurrentActivityExecutionSize: 100,
		// MaxConcurrentWorkflowTaskExecutionSize: 100,
	})

	// Register Workflows
	w.RegisterWorkflow(workflow.DAGWorkflow)

	// Register Activities
	w.RegisterActivity(activities.SimpleSyncActivity)
	w.RegisterActivity(activities.AsyncActivityExample)
	w.RegisterActivity(activities.WaitActivityExample)
	w.RegisterActivity(activities.ActivityThatCanFail)

	// Start the worker. This will block until the worker is stopped or an error occurs.
	err := w.Run(worker.InterruptCh()) // worker.InterruptCh() listens for OS signals like Ctrl+C
	if err != nil {
		logger.Fatal("Worker run failed", zap.Error(err))
	}
	logger.Info("Worker stopped.")
}

func startSampleWorkflow(c client.Client, logger *zap.Logger) {
	logger.Info("Starting sample DAGWorkflow execution from config.yaml...")

	yamlFile, err := os.ReadFile("config.yaml")
	if err != nil {
		logger.Fatal("Failed to read config.yaml", zap.Error(err))
	}

	rulesFile, err := os.ReadFile("rules.yaml") // Load rules.yaml
	if err != nil {
		// It's okay if rules.yaml doesn't exist, LoadFullWorkflowConfigFromYAMLs will handle empty rulesBytes
		logger.Warn("Failed to read rules.yaml, proceeding without external rules unless embedded in config.yaml itself", zap.Error(err))
		rulesFile = []byte{} // Empty bytes if file not found
	}

	cfg, err := workflow.LoadFullWorkflowConfigFromYAMLs(yamlFile, rulesFile)
	if err != nil {
		logger.Fatal("Failed to load workflow and rules config from YAML files", zap.Error(err))
	}

	workflowInput := workflow.DAGWorkflowInput{
		Config: cfg,
	}

	workflowOptions := client.StartWorkflowOptions{
		ID:        "dynamic-dag-workflow_" + time.Now().Format("20060102_150405"),
		TaskQueue: TaskQueueName,
		// WorkflowExecutionTimeout: time.Minute * 30, // Optional: overall timeout for the workflow
		// WorkflowRunTimeout: time.Minute * 20, // Optional: timeout for a single run
		// WorkflowTaskTimeout: time.Second * 30, // Optional: timeout for a workflow task
	}

	ctx := context.Background()
	run, err := c.ExecuteWorkflow(ctx, workflowOptions, workflow.DAGWorkflow, workflowInput)
	if err != nil {
		logger.Error("Failed to start workflow execution", zap.Error(err))
		return
	}

	logger.Info("Workflow execution started",
		zap.String("WorkflowID", run.GetID()),
		zap.String("RunID", run.GetRunID()))

	// Optionally, wait for the workflow to complete and get the result
	// var result map[string]interface{}
	// err = run.Get(ctx, &result)
	// if err != nil {
	// 	logger.Error("Workflow execution resulted in error", zap.Error(err))
	// } else {
	// 	logger.Info("Workflow completed successfully", zap.Any("result", result))
	// }
}

// Go Mod Init (Run this in terminal if not already done)
// go mod init dynamicworkflow
// go mod tidy
//
// To Run (from project root):
// 1. Start Temporal Server (e.g., `temporal server start-dev --ui-port 8080`)
// 2. Run the worker & starter:
//    `go run main.go`
// 3. Or run worker only:
//    `RUN_MODE=WORKER go run main.go`
// 4. Or run starter only (if a worker is already running):
//    `RUN_MODE=STARTER go run main.go`
//
// Check Temporal UI (localhost:8080 by default for dev server) to see the workflow.
