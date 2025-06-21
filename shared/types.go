package shared

// NodeType defines the type of a DAG node
type NodeType string

const (
	NodeTypeAsync NodeType = "async"
	NodeTypeWait  NodeType = "wait"
	NodeTypeSync  NodeType = "sync"
	// Add other node types as needed
)

// NodeState defines the current state of a node in the workflow
type NodeState string

const (
	NodeStatePending   NodeState = "pending"
	NodeStateRunning   NodeState = "running"
	NodeStateCompleted NodeState = "completed"
	NodeStateSkipped   NodeState = "skipped"
	NodeStateFailed    NodeState = "failed"
	NodeStateExpired   NodeState = "expired"
	NodeStateTimedOut  NodeState = "timedout" // Specifically for activity TTL
)

// Node represents a single node in the DAG
type Node struct {
	ID            string                 `json:"id"`
	Type          NodeType               `json:"type"`
	ActivityName  string                 `json:"activityName"`
	Params        map[string]interface{} `json:"params"`
	Dependencies  []string               `json:"dependencies"`
	TTLSeconds    int                    `json:"ttlSeconds"`    // Time-to-live for the activity execution (0 for no TTL)
	ExpirySeconds int                    `json:"expirySeconds"` // Expiry time for the node itself (0 for no expiry)
	RedoCondition string                 `json:"redoCondition"` // e.g., "output.status == 'failed'"
	SkipCondition string                 `json:"skipCondition"` // e.g., "input.priority < 5"
}

// WorkflowConfig defines the structure of the workflow to be executed
type WorkflowConfig struct {
	Nodes        map[string]Node `json:"nodes"` // Using a map for easy lookup by ID
	StartNodeIDs []string        `json:"startNodeIDs"`
}

// NodeStatus holds the runtime status of a node
type NodeStatus struct {
	State         NodeState
	Result        interface{}
	Error         error
	ScheduledTime int64 // Unix timestamp when the node was scheduled to run
	StartTime     int64 // Unix timestamp when the node actually started
	EndTime       int64 // Unix timestamp when the node finished (completed, failed, etc.)
	Attempt       int   // Current attempt number for redo
}
