package shared

// NodeType defines the type of a DAG node
type NodeType string

const (
	NodeTypeAsync       NodeType = "async"
	NodeTypeWait        NodeType = "wait"
	NodeTypeSync        NodeType = "sync"
	NodeTypeUserInput   NodeType = "user_input"
	// Add other node types as needed
)

// Rule defines a reusable condition expression
type Rule struct {
	ID          string `json:"id" yaml:"id"`
	Expression  string `json:"expression" yaml:"expression"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// NextNodeRule defines a rule (by ID) and a target node for conditional pathing
type NextNodeRule struct {
	RuleID       string `json:"ruleId" yaml:"ruleId"`             // ID of a rule defined in WorkflowConfig.Rules
	TargetNodeID string `json:"targetNodeId" yaml:"targetNodeId"` // The ID of the next node to execute if condition is met
}

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
	Params                map[string]interface{} `json:"params"`
	Dependencies          []string               `json:"dependencies"` // Prerequisites for this node to start
	ActivityTimeoutSeconds int                    `json:"activityTimeoutSeconds"` // Timeout for the node's activity execution
	ResultValiditySeconds int                    `json:"resultValiditySeconds"`  // How long the node's result is considered valid
	ExpirySeconds         int                    `json:"expirySeconds"`          // Expiry for the node itself before it starts (original meaning)
	RedoCondition         string                 `json:"redoCondition"`          // e.g., "output.status == 'failed'"
	SkipCondition         string                 `json:"skipCondition"`          // e.g., "input.priority < 5"
	NextNodeRules         []NextNodeRule         `json:"nextNodeRules"`          // Rules for conditional routing to next nodes
	SignalName            string                 `json:"signalName"`             // Signal name for user_input type nodes
}

// WorkflowConfig defines the structure of the workflow to be executed
type WorkflowConfig struct {
	Nodes        map[string]Node `json:"nodes" yaml:"nodes"` // Using a map for easy lookup by ID
	StartNodeIDs []string        `json:"startNodeIDs" yaml:"startNodeIDs"`
	Rules        map[string]Rule `json:"rules,omitempty" yaml:"rules,omitempty"` // Map of RuleID to Rule definition
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
