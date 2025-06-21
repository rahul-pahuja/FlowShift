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

// ValidityTimer represents an active validity timer
type ValidityTimer struct {
	TimerID         string
	SourceNodeID    string
	DependentNodeID string
	TimerFuture     workflow.Future
	CancelFunc      workflow.CancelFunc
}

// ValidityManager manages result validity timers for nodes
type ValidityManager struct {
	ctx           workflow.Context
	logger        log.Logger
	activeTimers  map[string]*ValidityTimer
	timerSelector workflow.Selector
	mu            sync.RWMutex
}

// NewValidityManager creates a new validity manager
func NewValidityManager(ctx workflow.Context, logger log.Logger) *ValidityManager {
	return &ValidityManager{
		ctx:           ctx,
		logger:        logger,
		activeTimers:  make(map[string]*ValidityTimer),
		timerSelector: workflow.NewSelector(ctx),
	}
}

// SetupValidityTimer sets up a result validity timer for a node
func (vm *ValidityManager) SetupValidityTimer(nodeID string, node shared.Node, status *shared.NodeStatus, activityOutput interface{}) {
	if node.ResultValiditySeconds <= 0 {
		return
	}

	// Find critical dependent that this timer should track
	criticalDependentID := vm.findCriticalDependent(nodeID, node, activityOutput)
	if criticalDependentID == "" {
		vm.logger.Debug("No critical dependent found for validity timer", zap.String("nodeID", nodeID))
		return
	}

	timerID := fmt.Sprintf("validity_%s_attempt_%d", nodeID, status.Attempt)
	validityDuration := time.Duration(node.ResultValiditySeconds) * time.Second

	vm.logger.Info("Setting up result validity timer",
		zap.String("sourceNode", nodeID),
		zap.String("dependentNode", criticalDependentID),
		zap.Duration("validityDuration", validityDuration))

	// Create cancellable context for the timer
	timerCtx, cancelFunc := workflow.WithCancel(vm.ctx)
	timerFuture := workflow.NewTimer(timerCtx, validityDuration)

	validityTimer := &ValidityTimer{
		TimerID:         timerID,
		SourceNodeID:    nodeID,
		DependentNodeID: criticalDependentID,
		TimerFuture:     timerFuture,
		CancelFunc:      cancelFunc,
	}

	vm.mu.Lock()
	vm.activeTimers[timerID] = validityTimer
	vm.mu.Unlock()

	// Add timer to selector for monitoring
	vm.timerSelector.AddFuture(timerFuture, func(f workflow.Future) {
		vm.handleValidityTimerExpiry(validityTimer)
	})
}

// findCriticalDependent finds the most critical dependent node for validity tracking
func (vm *ValidityManager) findCriticalDependent(nodeID string, node shared.Node, activityOutput interface{}) string {
	// For nodes with NextNodeRules, find the first rule that would be satisfied
	if len(node.NextNodeRules) > 0 {
		for _, ruleRef := range node.NextNodeRules {
			// In a real implementation, we would evaluate the condition here
			// For now, return the first target node
			return ruleRef.TargetNodeID
		}
	}

	// For nodes without rules, we'd need to look at the dependency graph
	// This is a simplified implementation
	return ""
}

// handleValidityTimerExpiry handles when a validity timer expires
func (vm *ValidityManager) handleValidityTimerExpiry(timer *ValidityTimer) {
	vm.logger.Warn("Result validity timer fired - source node result is stale",
		zap.String("timerID", timer.TimerID),
		zap.String("sourceNode", timer.SourceNodeID),
		zap.String("dependentNode", timer.DependentNodeID))

	// Remove from active timers
	vm.mu.Lock()
	delete(vm.activeTimers, timer.TimerID)
	vm.mu.Unlock()

	// In a full implementation, this would trigger node resets
	// For now, we just log the event
	vm.logger.Info("Validity timer expired - nodes should be reset",
		zap.String("sourceNode", timer.SourceNodeID),
		zap.String("dependentNode", timer.DependentNodeID))
}

// CancelValidityTimer cancels an active validity timer
func (vm *ValidityManager) CancelValidityTimer(timerID string) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	if timer, exists := vm.activeTimers[timerID]; exists {
		timer.CancelFunc()
		delete(vm.activeTimers, timerID)
		
		vm.logger.Info("Validity timer cancelled",
			zap.String("timerID", timerID),
			zap.String("sourceNode", timer.SourceNodeID))
	}
}

// CancelValidityTimersForNode cancels all validity timers for a specific source node
func (vm *ValidityManager) CancelValidityTimersForNode(nodeID string) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	for timerID, timer := range vm.activeTimers {
		if timer.SourceNodeID == nodeID {
			timer.CancelFunc()
			delete(vm.activeTimers, timerID)
			
			vm.logger.Info("Validity timer cancelled for node",
				zap.String("timerID", timerID),
				zap.String("nodeID", nodeID))
		}
	}
}

// GetActiveTimers returns information about active validity timers
func (vm *ValidityManager) GetActiveTimers() map[string]*ValidityTimer {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	// Return a copy to prevent external modification
	timers := make(map[string]*ValidityTimer)
	for id, timer := range vm.activeTimers {
		timerCopy := *timer
		timers[id] = &timerCopy
	}
	return timers
}

// HasActiveTimer checks if a node has an active validity timer
func (vm *ValidityManager) HasActiveTimer(nodeID string) bool {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	for _, timer := range vm.activeTimers {
		if timer.SourceNodeID == nodeID {
			return true
		}
	}
	return false
}

// GetTimerCount returns the number of active timers
func (vm *ValidityManager) GetTimerCount() int {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	return len(vm.activeTimers)
}