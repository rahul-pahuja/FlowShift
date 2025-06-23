package workflow

import (
	"sync"

	"flow-shift/shared"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

// DependencyManager handles dependency tracking and resolution for DAG nodes
type DependencyManager struct {
	dependentsMap   map[string][]string // nodeID -> list of nodes that depend on it
	dependencyCount map[string]int      // nodeID -> number of outstanding dependencies
	logger          log.Logger
	mu              sync.RWMutex
}

// NewDependencyManager creates a new dependency manager
func NewDependencyManager(nodes map[string]shared.Node, logger log.Logger) *DependencyManager {
	dm := &DependencyManager{
		dependentsMap:   make(map[string][]string),
		dependencyCount: make(map[string]int),
		logger:          logger,
	}

	dm.buildDependencyGraph(nodes)
	return dm
}

// buildDependencyGraph constructs the dependency graph from node definitions
func (dm *DependencyManager) buildDependencyGraph(nodes map[string]shared.Node) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	// Initialize dependency counts and build reverse dependency map
	for id, node := range nodes {
		dm.dependencyCount[id] = len(node.Dependencies)
		
		for _, depID := range node.Dependencies {
			dm.dependentsMap[depID] = append(dm.dependentsMap[depID], id)
		}
	}

	dm.logger.Info("Dependency graph built", 
		zap.Int("totalNodes", len(nodes)),
		zap.Any("dependencyCounts", dm.dependencyCount))
}

// GetReadyStartNodes returns start nodes that have no unmet dependencies
func (dm *DependencyManager) GetReadyStartNodes(startNodeIDs []string) []string {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	var readyNodes []string
	for _, nodeID := range startNodeIDs {
		if count, exists := dm.dependencyCount[nodeID]; exists && count == 0 {
			readyNodes = append(readyNodes, nodeID)
		} else if exists {
			dm.logger.Warn("Start node has unmet dependencies", 
				zap.String("nodeID", nodeID), 
				zap.Int("dependencies", count))
		} else {
			dm.logger.Warn("Start node not found in dependency graph", zap.String("nodeID", nodeID))
		}
	}

	return readyNodes
}

// GetOrphanedNodes returns nodes with no dependencies that aren't in start nodes
func (dm *DependencyManager) GetOrphanedNodes(startNodeIDs []string) []string {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	startNodeSet := make(map[string]bool)
	for _, nodeID := range startNodeIDs {
		startNodeSet[nodeID] = true
	}

	var orphanedNodes []string
	for nodeID, count := range dm.dependencyCount {
		if count == 0 && !startNodeSet[nodeID] {
			orphanedNodes = append(orphanedNodes, nodeID)
		}
	}

	return orphanedNodes
}

// GetDependents returns all nodes that depend on the given node
func (dm *DependencyManager) GetDependents(nodeID string) []string {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	dependents := dm.dependentsMap[nodeID]
	// Return a copy to prevent external modification
	result := make([]string, len(dependents))
	copy(result, dependents)
	return result
}

// DecrementDependency decrements the dependency count for a node and queues it if ready
func (dm *DependencyManager) DecrementDependency(nodeID, sourceNodeID string, nodeStatuses map[string]*shared.NodeStatus, readyQueue chan string, ctx workflow.Context) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if _, exists := dm.dependencyCount[nodeID]; exists {
		// Verify this is a legitimate dependency
		if !dm.isLegitimateSource(nodeID, sourceNodeID) {
			dm.logger.Warn("Attempted to decrement dependency from non-declared source",
				zap.String("targetNode", nodeID),
				zap.String("sourceNode", sourceNodeID))
			return
		}

		dm.dependencyCount[nodeID]--
		newCount := dm.dependencyCount[nodeID]

		dm.logger.Info("Dependency count decremented",
			zap.String("nodeID", nodeID),
			zap.String("sourceNode", sourceNodeID),
			zap.Int("newCount", newCount))

		// Check if node is ready to be queued
		if newCount == 0 {
			if status, statusExists := nodeStatuses[nodeID]; statusExists && status.State == shared.NodeStatePending {
				dm.logger.Info("All dependencies met, queuing node", zap.String("nodeID", nodeID))
				status.ScheduledTime = workflow.Now(ctx).Unix()
				
				// Non-blocking send to ready queue
				select {
				case readyQueue <- nodeID:
				default:
					dm.logger.Warn("Ready queue full, node scheduling delayed", zap.String("nodeID", nodeID))
					// Could implement a retry mechanism here
				}
			}
		}
	}
}

// isLegitimateSource checks if sourceNodeID is a legitimate dependency source for nodeID
func (dm *DependencyManager) isLegitimateSource(nodeID, sourceNodeID string) bool {
	dependents := dm.dependentsMap[sourceNodeID]
	for _, dependentID := range dependents {
		if dependentID == nodeID {
			return true
		}
	}
	return false
}

// GetDependencyCount returns the current dependency count for a node
func (dm *DependencyManager) GetDependencyCount(nodeID string) int {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if count, exists := dm.dependencyCount[nodeID]; exists {
		return count
	}
	return -1 // Node not found
}

// GetDependencyStatus returns a snapshot of the current dependency state
func (dm *DependencyManager) GetDependencyStatus() map[string]int {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	// Return a copy to prevent external modification
	status := make(map[string]int)
	for nodeID, count := range dm.dependencyCount {
		status[nodeID] = count
	}
	return status
}

// ResetDependency resets the dependency count for a node (used in redo scenarios)
func (dm *DependencyManager) ResetDependency(nodeID string, originalDependencies []string) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dm.dependencyCount[nodeID] = len(originalDependencies)
	dm.logger.Info("Dependency count reset", 
		zap.String("nodeID", nodeID), 
		zap.Int("dependencyCount", len(originalDependencies)))
}