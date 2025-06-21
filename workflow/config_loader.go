package workflow

import (
	"flow-shift/shared"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3" // Added for YAML parsing
)

// validateWorkflowConfig performs basic validation on the loaded config.
func validateWorkflowConfig(config *shared.WorkflowConfig) error {
	if len(config.Nodes) == 0 {
		return fmt.Errorf("workflow config must contain at least one node")
	}
	if len(config.StartNodeIDs) == 0 {
		return fmt.Errorf("workflow config must specify at least one StartNodeID")
	}
	for _, startID := range config.StartNodeIDs {
		if _, ok := config.Nodes[startID]; !ok {
			return fmt.Errorf("StartNodeID '%s' not found in nodes definition", startID)
		}
	}
	// TODO: Add more validation:
	// - Check for circular dependencies in static Dependencies.
	// - Validate that ActivityName is non-empty for relevant node types.
	// - Validate that SignalName is non-empty for UserInput node types.
	// - Validate that TargetNodeID in NextNodeRules exist in Nodes map.
	// - Ensure dependencies listed in Node.Dependencies exist in Nodes map.

	// Validate RuleIDs in NextNodeRules
	for nodeID, node := range config.Nodes {
		for i, ruleRef := range node.NextNodeRules {
			if ruleRef.RuleID == "" {
				return fmt.Errorf("node '%s', nextNodeRule #%d: RuleID is empty", nodeID, i)
			}
			if config.Rules == nil { // Should not happen if loader initializes it
				return fmt.Errorf("node '%s', nextNodeRule #%d: WorkflowConfig.Rules is nil, cannot validate RuleID '%s'", nodeID, i, ruleRef.RuleID)
			}
			if _, ok := config.Rules[ruleRef.RuleID]; !ok {
				return fmt.Errorf("node '%s', nextNodeRule #%d: RuleID '%s' not found in defined Rules", nodeID, i, ruleRef.RuleID)
			}
			if _, ok := config.Nodes[ruleRef.TargetNodeID]; !ok {
                 return fmt.Errorf("node '%s', nextNodeRule #%d (RuleID '%s'): TargetNodeID '%s' not found in defined Nodes", nodeID, i, ruleRef.RuleID, ruleRef.TargetNodeID)
            }
		}
		// Validate static dependencies exist
		for i, depID := range node.Dependencies {
			if _, ok := config.Nodes[depID]; !ok {
				return fmt.Errorf("node '%s', dependency #%d: NodeID '%s' not found in defined Nodes", nodeID, i, depID)
			}
		}
	}

	return nil
}

// LoadWorkflowConfigFromJSONBytes parses JSON bytes into a WorkflowConfig struct.
// Note: Does not load external rules by default.
func LoadWorkflowConfigFromJSONBytes(jsonData []byte) (shared.WorkflowConfig, error) {
	var config shared.WorkflowConfig
	err := json.Unmarshal(jsonData, &config)
	if err != nil {
		return shared.WorkflowConfig{}, fmt.Errorf("failed to unmarshal workflow config JSON: %w", err)
	}
	// Rules would need to be loaded separately or embedded if using JSON directly this way.
	if err := validateWorkflowConfig(&config); err != nil { // validateWorkflowConfig will also need to check RuleIDs
		return shared.WorkflowConfig{}, fmt.Errorf("invalid workflow config: %w", err)
	}
	return config, nil
}

// LoadWorkflowConfigFromYAMLBytes parses YAML bytes for the main DAG structure.
// It does NOT load rules from a separate file; that's handled by LoadFullWorkflowConfigFromYAMLs.
func LoadWorkflowConfigFromYAMLBytes(yamlData []byte) (shared.WorkflowConfig, error) {
	var config shared.WorkflowConfig
	err := yaml.Unmarshal(yamlData, &config)
	if err != nil {
		return shared.WorkflowConfig{}, fmt.Errorf("failed to unmarshal workflow config YAML: %w", err)
	}
	if err := validateWorkflowConfig(&config); err != nil { // This validation will be enhanced
		return shared.WorkflowConfig{}, fmt.Errorf("invalid workflow config from YAML bytes: %w", err)
	}
	return config, nil
}

// LoadRulesFromYAMLBytes parses YAML bytes into a map of Rule definitions.
// Expects YAML structure like:
// rules:
//   RuleID1: {id: RuleID1, expression: "...", ...}
//   RuleID2: {id: RuleID2, expression: "...", ...}
func LoadRulesFromYAMLBytes(yamlData []byte) (map[string]shared.Rule, error) {
	var rulesWrapper struct {
		Rules map[string]shared.Rule `yaml:"rules"`
	}
	err := yaml.Unmarshal(yamlData, &rulesWrapper)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal rules YAML: %w", err)
	}
	// Validate that map key matches Rule.ID
	for k, v := range rulesWrapper.Rules {
		if k != v.ID {
			return nil, fmt.Errorf("rule ID mismatch in YAML: map key '%s' does not match rule.ID '%s'", k, v.ID)
		}
	}
	return rulesWrapper.Rules, nil
}

// LoadFullWorkflowConfigFromYAMLs loads the main DAG config and associated rules from byte slices.
func LoadFullWorkflowConfigFromYAMLs(dagYAMLBytes []byte, rulesYAMLBytes []byte) (shared.WorkflowConfig, error) {
	config, err := LoadWorkflowConfigFromYAMLBytes(dagYAMLBytes)
	if err != nil {
		return shared.WorkflowConfig{}, err // Error already descriptive
	}

	if len(rulesYAMLBytes) > 0 {
		rules, err := LoadRulesFromYAMLBytes(rulesYAMLBytes)
		if err != nil {
			return shared.WorkflowConfig{}, fmt.Errorf("failed to load rules: %w", err)
		}
		config.Rules = rules
	} else {
		config.Rules = make(map[string]shared.Rule) // Ensure Rules map is initialized
	}

	// Re-validate after rules are attached, specifically for RuleID references
	if err := validateWorkflowConfig(&config); err != nil {
		return shared.WorkflowConfig{}, fmt.Errorf("invalid workflow config after merging rules: %w", err)
	}

	return config, nil
}


// GetSampleWorkflowConfigJSON returns a sample JSON string for a workflow configuration.
// DEPRECATED: Prefer using config.yaml and LoadWorkflowConfigFromYAMLBytes.
// This is kept for potential compatibility or specific JSON use cases.
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
