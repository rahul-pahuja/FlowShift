package workflow

import (
	"sync"
	"time"

	"go.temporal.io/sdk/log"
)

// DAGExecutionMetrics provides detailed metrics for DAG execution
type DAGExecutionMetrics struct {
	TotalNodes              int
	CompletedOrSkippedNodes int
	ProcessingNodes         int
	FailedNodes             int
	SkippedNodes            int
	TimedOutNodes           int
	ExpiredNodes            int
	StartTime               time.Time
	EndTime                 time.Time
	ExecutionDuration       time.Duration
	NodeExecutionTimes      map[string]time.Duration
	NodeAttempts            map[string]int
	ActiveSignals           map[string]bool
	ActiveTimers            int
	mu                      sync.RWMutex
}

// NewDAGExecutionMetrics creates a new metrics instance
func NewDAGExecutionMetrics(totalNodes int) *DAGExecutionMetrics {
	return &DAGExecutionMetrics{
		TotalNodes:         totalNodes,
		StartTime:          time.Now(),
		NodeExecutionTimes: make(map[string]time.Duration),
		NodeAttempts:       make(map[string]int),
		ActiveSignals:      make(map[string]bool),
	}
}

// RecordNodeStart records when a node starts processing
func (m *DAGExecutionMetrics) RecordNodeStart(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ProcessingNodes++
}

// RecordNodeCompletion records when a node completes successfully
func (m *DAGExecutionMetrics) RecordNodeCompletion(nodeID string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.ProcessingNodes--
	m.CompletedOrSkippedNodes++
	m.NodeExecutionTimes[nodeID] = duration
}

// RecordNodeSkipped records when a node is skipped
func (m *DAGExecutionMetrics) RecordNodeSkipped(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.ProcessingNodes--
	m.CompletedOrSkippedNodes++
	m.SkippedNodes++
}

// RecordNodeFailed records when a node fails
func (m *DAGExecutionMetrics) RecordNodeFailed(nodeID string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.ProcessingNodes--
	m.CompletedOrSkippedNodes++
	m.FailedNodes++
	m.NodeExecutionTimes[nodeID] = duration
}

// RecordNodeTimedOut records when a node times out
func (m *DAGExecutionMetrics) RecordNodeTimedOut(nodeID string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.ProcessingNodes--
	m.CompletedOrSkippedNodes++
	m.TimedOutNodes++
	m.NodeExecutionTimes[nodeID] = duration
}

// RecordNodeExpired records when a node expires
func (m *DAGExecutionMetrics) RecordNodeExpired(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.ProcessingNodes--
	m.CompletedOrSkippedNodes++
	m.ExpiredNodes++
}

// RecordNodeAttempt records an attempt for a node
func (m *DAGExecutionMetrics) RecordNodeAttempt(nodeID string, attempt int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NodeAttempts[nodeID] = attempt
}

// RecordSignalActive records an active signal
func (m *DAGExecutionMetrics) RecordSignalActive(signalName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ActiveSignals[signalName] = true
}

// RecordSignalCompleted records a completed signal
func (m *DAGExecutionMetrics) RecordSignalCompleted(signalName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.ActiveSignals, signalName)
}

// RecordTimerActive increments active timer count
func (m *DAGExecutionMetrics) RecordTimerActive() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ActiveTimers++
}

// RecordTimerCompleted decrements active timer count
func (m *DAGExecutionMetrics) RecordTimerCompleted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ActiveTimers > 0 {
		m.ActiveTimers--
	}
}

// FinishExecution marks the execution as finished
func (m *DAGExecutionMetrics) FinishExecution() {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.EndTime = time.Now()
	m.ExecutionDuration = m.EndTime.Sub(m.StartTime)
}

// GetSnapshot returns a snapshot of current metrics
func (m *DAGExecutionMetrics) GetSnapshot() DAGExecutionMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	// Create a deep copy
	snapshot := *m
	snapshot.NodeExecutionTimes = make(map[string]time.Duration)
	snapshot.NodeAttempts = make(map[string]int)
	snapshot.ActiveSignals = make(map[string]bool)
	
	for k, v := range m.NodeExecutionTimes {
		snapshot.NodeExecutionTimes[k] = v
	}
	for k, v := range m.NodeAttempts {
		snapshot.NodeAttempts[k] = v
	}
	for k, v := range m.ActiveSignals {
		snapshot.ActiveSignals[k] = v
	}
	
	return snapshot
}

// LogMetrics logs current metrics state
func (m *DAGExecutionMetrics) LogMetrics(logger log.Logger, level string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	keyvals := []interface{}{
		"totalNodes", m.TotalNodes,
		"completedOrSkipped", m.CompletedOrSkippedNodes,
		"processing", m.ProcessingNodes,
		"failed", m.FailedNodes,
		"skipped", m.SkippedNodes,
		"timedOut", m.TimedOutNodes,
		"expired", m.ExpiredNodes,
		"executionDuration", m.ExecutionDuration,
		"activeTimers", m.ActiveTimers,
		"activeSignals", len(m.ActiveSignals),
	}
	
	switch level {
	case "error":
		logger.Error("DAG Execution Metrics", keyvals...)
	case "warn":
		logger.Warn("DAG Execution Metrics", keyvals...)
	case "debug":
		logger.Debug("DAG Execution Metrics", keyvals...)
	default:
		logger.Info("DAG Execution Metrics", keyvals...)
	}
}

// GetCompletionPercentage returns the completion percentage
func (m *DAGExecutionMetrics) GetCompletionPercentage() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if m.TotalNodes == 0 {
		return 100.0
	}
	return float64(m.CompletedOrSkippedNodes) / float64(m.TotalNodes) * 100.0
}

// GetAverageExecutionTime returns the average execution time for completed nodes
func (m *DAGExecutionMetrics) GetAverageExecutionTime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if len(m.NodeExecutionTimes) == 0 {
		return 0
	}
	
	var total time.Duration
	for _, duration := range m.NodeExecutionTimes {
		total += duration
	}
	
	return total / time.Duration(len(m.NodeExecutionTimes))
}

// IsExecutionComplete checks if execution is complete
func (m *DAGExecutionMetrics) IsExecutionComplete() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.CompletedOrSkippedNodes >= m.TotalNodes
}

// IsDeadlocked checks if execution appears deadlocked
func (m *DAGExecutionMetrics) IsDeadlocked() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.CompletedOrSkippedNodes < m.TotalNodes &&
		m.ProcessingNodes == 0 &&
		len(m.ActiveSignals) == 0 &&
		m.ActiveTimers == 0
}