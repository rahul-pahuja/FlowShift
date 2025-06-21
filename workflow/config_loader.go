package workflow

import (
	"dynamicworkflow/shared"
	"encoding/json"
	"fmt"
)

// LoadWorkflowConfigFromJSONString parses a JSON string into a WorkflowConfig struct.
// In a real application, this JSON might come from a file, a database, or an API.
func LoadWorkflowConfigFromJSONString(jsonString string) (shared.WorkflowConfig, error) {
	var config shared.WorkflowConfig
	err := json.Unmarshal([]byte(jsonString), &config)
	if err != nil {
		return shared.WorkflowConfig{}, fmt.Errorf("failed to unmarshal workflow config JSON: %w", err)
	}

	// Basic validation (can be expanded)
	if len(config.Nodes) == 0 {
		return shared.WorkflowConfig{}, fmt.Errorf("workflow config must contain at least one node")
	}
	if len(config.StartNodeIDs) == 0 {
		return shared.WorkflowConfig{}, fmt.Errorf("workflow config must specify at least one StartNodeID")
	}
	for _, startID := range config.StartNodeIDs {
		if _, ok := config.Nodes[startID]; !ok {
			return shared.WorkflowConfig{}, fmt.Errorf("StartNodeID '%s' not found in nodes definition", startID)
		}
	}
	// Could add more validation: e.g. check for circular dependencies, valid activity names etc.

	return config, nil
}

// GetSampleWorkflowConfigJSON returns a sample JSON string for a workflow configuration.
// This is useful for testing and demonstration.
func GetSampleWorkflowConfigJSON() string {
	// Sample DAG:
	//    A -> B -> D
	//      \-> C -> E
	// F (no dependencies)
	// B, C run in parallel after A. D runs after B. E runs after C.
	// F runs in parallel with A path.
	sampleConfig := shared.WorkflowConfig{
		StartNodeIDs: []string{"A", "F"},
		Nodes: map[string]shared.Node{
			"A": {
				ID:           "A",
				Type:         shared.NodeTypeSync,
				ActivityName: "SimpleSyncActivity",
				Params:       map[string]interface{}{"message": "Node A executed"},
				Dependencies: []string{},
				TTLSeconds:   60,
			},
			"B": {
				ID:           "B",
				Type:         shared.NodeTypeSync,
				ActivityName: "SimpleSyncActivity",
				Params:       map[string]interface{}{"message": "Node B executed"},
				Dependencies: []string{"A"},
				TTLSeconds:   60,
			},
			"C": {
				ID:           "C",
				Type:         shared.NodeTypeAsync, // Example type
				ActivityName: "AsyncActivityExample",
				Params:       map[string]interface{}{"message": "Node C executed", "_skip": false},
				Dependencies: []string{"A"},
				TTLSeconds:   120,
			},
			"D": {
				ID:           "D",
				Type:         shared.NodeTypeSync,
				ActivityName: "SimpleSyncActivity",
				Params:       map[string]interface{}{"message": "Node D executed"},
				Dependencies: []string{"B"},
				TTLSeconds:   30,
			},
			"E": {
				ID:           "E",
				Type:         shared.NodeTypeWait, // Example type
				ActivityName: "WaitActivityExample",
				Params:       map[string]interface{}{"message": "Node E executed", "duration": 5},
				Dependencies: []string{"C"},
				TTLSeconds:   180, // Needs to be > wait duration + heartbeat overhead
				ExpirySeconds: 300, // Node E itself can expire if not started within 5 mins
			},
			"F": {
				ID:           "F",
				Type:         shared.NodeTypeSync,
				ActivityName: "ActivityThatCanFail",
				Params:       map[string]interface{}{"message": "Node F executed", "forceFail": true},
				Dependencies: []string{},
				TTLSeconds:   60,
				RedoCondition: "on_failure",
			},
			"G_skipped": { // Node to demonstrate skip
				ID:            "G_skipped",
				Type:          shared.NodeTypeSync,
				ActivityName:  "SimpleSyncActivity",
				Params:        map[string]interface{}{"message": "Node G should be skipped", "_skip": true},
				Dependencies:  []string{"A"}, // Depends on A
				SkipCondition: "params._skip == true", // Just for info, not evaluated yet
			},
		},
	}
	jsonBytes, _ := json.MarshalIndent(sampleConfig, "", "  ")
	return string(jsonBytes)
}
