package workflow

import (
	"context" // Required for mock function signatures
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"flow-shift/activities"
	"flow-shift/shared"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

type UnitTestSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
	env *testsuite.TestWorkflowEnvironment
}

func TestUnitTestSuite(t *testing.T) {
	suite.Run(t, new(UnitTestSuite))
}

func (s *UnitTestSuite) SetupTest() {
	s.env = s.NewTestWorkflowEnvironment()
	s.env.RegisterActivity(activities.SimpleSyncActivity)
	s.env.RegisterActivity(activities.AsyncActivityExample)
	s.env.RegisterActivity(activities.WaitActivityExample)
	s.env.RegisterActivity(activities.ActivityThatCanFail)
	// Register mock activities used in tests by specific names using env.RegisterActivity
	s.env.RegisterActivityWithOptions(func(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
		// Default mock for DecisionActivity, specific tests can override with s.env.OnActivity
		if choice, ok := params["choice"].(string); ok {
			return map[string]interface{}{"decision": strings.ToUpper(choice)}, nil
		}
		return map[string]interface{}{"decision": "UNKNOWN"}, nil
	}, activity.RegisterOptions{Name: "DecisionActivity"})
	s.env.RegisterActivityWithOptions(func(ctx context.Context) (string, error) { return "Path1ActivityOutput", nil }, activity.RegisterOptions{Name: "Path1Activity"})
	s.env.RegisterActivityWithOptions(func(ctx context.Context) (string, error) { return "Path2ActivityOutput", nil }, activity.RegisterOptions{Name: "Path2Activity"})
	s.env.RegisterActivityWithOptions(func(ctx context.Context, params map[string]interface{}) (string, error) { return "PostSignalActivityOutput", nil }, activity.RegisterOptions{Name: "PostSignalActivity"})
	s.env.RegisterActivityWithOptions(func(ctx context.Context, params map[string]interface{}) (string, error) { return "LongRunningOutput", nil }, activity.RegisterOptions{Name: "LongRunningActivity"})
	s.env.RegisterActivityWithOptions(func(ctx context.Context, params map[string]interface{}) (string, error) { return "ShortActivityOutput", nil }, activity.RegisterOptions{Name: "ShortActivity"})
	s.env.RegisterActivityWithOptions(func(ctx context.Context, params map[string]interface{}) (string, error) { return "NeverRunOutput", nil }, activity.RegisterOptions{Name: "NeverRunActivity"})
}

func (s *UnitTestSuite) AfterTest(suiteName, testName string) {
	s.env.AssertExpectations(s.T())
}

// --- Test Cases (Existing tests updated, new tests to be added) ---

func (s *UnitTestSuite) Test_BasicDAG_SuccessfulExecution_WithNextNodeRules() {
	ruleStatusDoneID := "StatusIsDone"
	ruleTrueID := "AlwaysTrue"
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{"A"},
		Nodes: map[string]shared.Node{
			"A": {
				ID:           "A",
				ActivityName: "SimpleSyncActivity",
				Params:       map[string]interface{}{"data": "A_data"},
				NextNodeRules: []shared.NextNodeRule{
					{RuleID: ruleStatusDoneID, TargetNodeID: "B"},
					{RuleID: ruleStatusDoneID, TargetNodeID: "C"},
				},
			},
			"B": {ID: "B", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"data": "B_data"}, Dependencies: []string{"A"}, NextNodeRules: []shared.NextNodeRule{{RuleID: ruleTrueID, TargetNodeID: "D"}}},
			"C": {ID: "C", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"data": "C_data"}, Dependencies: []string{"A"}, NextNodeRules: []shared.NextNodeRule{{RuleID: ruleTrueID, TargetNodeID: "D"}}},
			"D": {ID: "D", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"data": "D_data"}, Dependencies: []string{"B", "C"}},
		},
		Rules: map[string]shared.Rule{
			ruleStatusDoneID: {ID: ruleStatusDoneID, Expression: "output.status == 'done'"},
			ruleTrueID:       {ID: ruleTrueID, Expression: "true"},
		},
	}
	input := DAGWorkflowInput{Config: config}

	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "A_data"}).Return(map[string]interface{}{"status": "done"}, nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "B_data"}).Return("Output from B", nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "C_data"}).Return("Output from C", nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "D_data"}).Return("Output from D", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.NotNil(results["A"])
	s.Equal("Output from B", results["B"])
	s.Equal("Output from C", results["C"])
	s.Equal("Output from D", results["D"])
}

func (s *UnitTestSuite) Test_NodeSkipped_WithNextNodeRules() {
	ruleTrueID := "AlwaysTrue"
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{"A"},
		Nodes: map[string]shared.Node{
			"A":      {ID: "A", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"data": "A_data"}, NextNodeRules: []shared.NextNodeRule{{RuleID: ruleTrueID, TargetNodeID: "B_skip"}}},
			"B_skip": {ID: "B_skip", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"_skip": true}, Dependencies: []string{"A"}, NextNodeRules: []shared.NextNodeRule{{RuleID: ruleTrueID, TargetNodeID: "C"}}},
			"C":      {ID: "C", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"data": "C_data"}, Dependencies: []string{"B_skip"}},
		},
		Rules: map[string]shared.Rule{
			ruleTrueID: {ID: ruleTrueID, Expression: "true"},
		},
	}
	input := DAGWorkflowInput{Config: config}
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "A_data"}).Return("Output from A", nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, map[string]interface{}{"data": "C_data"}).Return("Output from C", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Equal("SKIPPED", results["B_skip"])
	s.Equal("Output from C", results["C"])
}

func (s *UnitTestSuite) Test_NodeFailure_RedoSuccess_WithActivityTimeoutSeconds() {
	ruleTrueID := "AlwaysTrue"
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{"FailNode"},
		Nodes: map[string]shared.Node{
			"FailNode": {ID: "FailNode", ActivityName: "ActivityThatCanFail", Params: map[string]interface{}{"id": "FailNode"}, RedoCondition: "on_failure", ActivityTimeoutSeconds: 10, NextNodeRules: []shared.NextNodeRule{{RuleID: ruleTrueID, TargetNodeID: "SuccessNode"}}},
			"SuccessNode": {ID: "SuccessNode", ActivityName: "SimpleSyncActivity", Params: map[string]interface{}{"id": "SuccessNode"}, Dependencies: []string{"FailNode"}},
		},
		Rules: map[string]shared.Rule{
			ruleTrueID: {ID: ruleTrueID, Expression: "true"},
		},
	}
	input := DAGWorkflowInput{Config: config}
	s.env.OnActivity("ActivityThatCanFail", mock.Anything, mock.Anything).Return("", errors.New("simulated error")).Once()
	s.env.OnActivity("ActivityThatCanFail", mock.Anything, mock.Anything).Return("Output from FailNode (2nd attempt)", nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.Anything).Return("Output from SuccessNode", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
}

func (s *UnitTestSuite) Test_NodeActivityTimeout() {
	nodeID, depID, ruleTrueID := "TimeoutNode", "DependentNode", "AlwaysTrue"
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{nodeID},
		Nodes: map[string]shared.Node{
			nodeID: {ID: nodeID, ActivityName: "LongRunningActivity", ActivityTimeoutSeconds: 1, NextNodeRules: []shared.NextNodeRule{{RuleID: ruleTrueID, TargetNodeID: depID}}},
			depID:  {ID: depID, ActivityName: "SimpleSyncActivity", Dependencies: []string{nodeID}},
		},
		Rules: map[string]shared.Rule{
			ruleTrueID: {ID: ruleTrueID, Expression: "true"},
		},
	}
	input := DAGWorkflowInput{Config: config}
	s.env.OnActivity("LongRunningActivity", mock.Anything, mock.Anything).Return("", errors.New("simulated timeout")).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.Anything).Return("Output from DependentNode", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	err := s.env.GetWorkflowError()
	s.Error(err)
	if err != nil {
		s.Contains(err.Error(), fmt.Sprintf("node %s %s", nodeID, shared.NodeStateTimedOut))
	}
}

func (s *UnitTestSuite) Test_NodeExpiry_BeforeActivityStart() {
	s.env.SetStartTime(time.Now())
	nodeA, nodeToExpire, nodeC, ruleTrueID := "A", "ExpireNode", "C", "AlwaysTrue"
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{nodeA},
		Nodes: map[string]shared.Node{
			nodeA:        {ID: nodeA, ActivityName: "ShortActivity", NextNodeRules: []shared.NextNodeRule{{RuleID: ruleTrueID, TargetNodeID: nodeToExpire}}},
			nodeToExpire: {ID: nodeToExpire, ActivityName: "NeverRunActivity", Dependencies: []string{nodeA}, ExpirySeconds: 2, NextNodeRules: []shared.NextNodeRule{{RuleID: ruleTrueID, TargetNodeID: nodeC}}},
			nodeC:        {ID: nodeC, ActivityName: "SimpleSyncActivity", Dependencies: []string{nodeToExpire}},
		},
		Rules: map[string]shared.Rule{
			ruleTrueID: {ID: ruleTrueID, Expression: "true"},
		},
	}
	input := DAGWorkflowInput{Config: config}
	s.env.OnActivity("ShortActivity", mock.Anything, mock.Anything).Return("Output from A", nil).Once()
	s.env.RegisterDelayedCallback(func() { 
		// Simulate time advancement by setting the time manually
		s.env.SetStartTime(time.Now().Add(-3 * time.Second))
	}, 1*time.Millisecond)
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.Anything).Return("Output from C", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Nil(results[nodeToExpire])
	s.Equal("Output from C", results[nodeC])
}

// --- New Test Cases ---

func (s *UnitTestSuite) Test_ConditionalPath_Path1Taken() {
	rulePath1ID, rulePath2ID := "Path1Rule", "Path2Rule"
	config := shared.WorkflowConfig{
		StartNodeIDs: []string{"Start"},
		Nodes: map[string]shared.Node{
			"Start": {ID: "Start", ActivityName: "DecisionActivity", Params: map[string]interface{}{"choice": "path1"},
				NextNodeRules: []shared.NextNodeRule{
					{RuleID: rulePath1ID, TargetNodeID: "Path1Node"},
					{RuleID: rulePath2ID, TargetNodeID: "Path2Node"},
				}},
			"Path1Node": {ID: "Path1Node", ActivityName: "Path1Activity", Dependencies: []string{"Start"}},
			"Path2Node": {ID: "Path2Node", ActivityName: "Path2Activity", Dependencies: []string{"Start"}},
		},
		Rules: map[string]shared.Rule{
			rulePath1ID: {ID: rulePath1ID, Expression: "output.decision == 'PATH1'"},
			rulePath2ID: {ID: rulePath2ID, Expression: "output.decision == 'PATH2'"},
		},
	}
	input := DAGWorkflowInput{Config: config}
	s.env.OnActivity("DecisionActivity", mock.Anything, mock.Anything).Return(map[string]interface{}{"decision": "PATH1"}, nil).Once()
	s.env.OnActivity("Path1Activity", mock.Anything, mock.Anything).Return("Path1 Output", nil).Once()

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Equal("Path1 Output", results["Path1Node"])
	s.Nil(results["Path2Node"])
}

func (s *UnitTestSuite) Test_UserInputNode_ReceivesSignal() {
	nodeID := "InputNode"
	signalName := "MyTestSignal"
	signalPayload := map[string]interface{}{"signal_data": "important_value"}

	config := shared.WorkflowConfig{
		StartNodeIDs: []string{nodeID},
		Nodes: map[string]shared.Node{
			nodeID: {
				ID:           nodeID,
				Type:         shared.NodeTypeUserInput,
				ActivityName: "PostSignalActivity",
				SignalName:   signalName,
				Params:       map[string]interface{}{"original_param": "original_value"},
			},
		},
		Rules: make(map[string]shared.Rule), // Initialize empty rules map
	}
	input := DAGWorkflowInput{Config: config}
	expectedParams := map[string]interface{}{"original_param": "original_value", "signal_data": "important_value"}
	s.env.OnActivity("PostSignalActivity", mock.Anything, expectedParams).Return("Activity Done", nil).Once()

	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(signalName, signalPayload)
	}, 1*time.Millisecond) // Shortened delay for test speed

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Equal("Activity Done", results[nodeID])
}

func (s *UnitTestSuite) Test_ResultValidity_DependentCompletesInTime() {
	sourceNodeID, dependentNodeID, sourceSignal, dependentSignal, ruleTrueID :=
		"SourceValidityNode", "DependentSignalNode", "SourceSignal", "DependentSignal", "AlwaysTrue"

	config := shared.WorkflowConfig{
		StartNodeIDs: []string{sourceNodeID},
		Nodes: map[string]shared.Node{
			sourceNodeID: {
				ID:                    sourceNodeID,
				Type:                  shared.NodeTypeUserInput,
				ActivityName:          "SimpleSyncActivity",
				SignalName:            sourceSignal,
				ResultValiditySeconds: 5,
				Params:                map[string]interface{}{"id": sourceNodeID},
				NextNodeRules:         []shared.NextNodeRule{{RuleID: ruleTrueID, TargetNodeID: dependentNodeID}},
			},
			dependentNodeID: {
				ID:           dependentNodeID,
				Type:         shared.NodeTypeUserInput,
				ActivityName: "SimpleSyncActivity",
				SignalName:   dependentSignal,
				Params:       map[string]interface{}{"id": dependentNodeID},
				Dependencies: []string{sourceNodeID},
			},
		},
		Rules: map[string]shared.Rule{
			ruleTrueID: {ID: ruleTrueID, Expression: "true"},
		},
	}
	input := DAGWorkflowInput{Config: config}

	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.MatchedBy(func(params map[string]interface{}) bool { return params["id"] == sourceNodeID })).Return("SourceOutput", nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.MatchedBy(func(params map[string]interface{}) bool { return params["id"] == dependentNodeID })).Return("DependentOutput", nil).Once()

	s.env.RegisterDelayedCallback(func() { s.env.SignalWorkflow(sourceSignal, map[string]interface{}{"sdata": "src"}) }, 1*time.Millisecond)
	s.env.RegisterDelayedCallback(func() { s.env.SignalWorkflow(dependentSignal, map[string]interface{}{"sdata": "dep"}) }, 2*time.Millisecond)

	s.env.ExecuteWorkflow(DAGWorkflow, input)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Equal("SourceOutput", results[sourceNodeID])
	s.Equal("DependentOutput", results[dependentNodeID])
}

func (s *UnitTestSuite) Test_ResultValidity_TimerFires_ResetsNodes() {
	sourceNodeID, dependentNodeID, sourceSignal, dependentSignal, ruleTrueID :=
		"SourceStaleNode", "DependentWaitsTooLongNode", "SourceStaleSignal", "DepWaitsSignal", "AlwaysTrue"

	config := shared.WorkflowConfig{
		StartNodeIDs: []string{sourceNodeID},
		Nodes: map[string]shared.Node{
			sourceNodeID: {
				ID:                    sourceNodeID,
				Type:                  shared.NodeTypeUserInput,
				ActivityName:          "SimpleSyncActivity",
				SignalName:            sourceSignal,
				ResultValiditySeconds: 2,
				Params:                map[string]interface{}{"id": sourceNodeID}, // Attempt will be tracked by workflow
				NextNodeRules:         []shared.NextNodeRule{{RuleID: ruleTrueID, TargetNodeID: dependentNodeID}},
			},
			dependentNodeID: {
				ID:           dependentNodeID,
				Type:         shared.NodeTypeUserInput,
				ActivityName: "SimpleSyncActivity",
				SignalName:   dependentSignal,
				Params:       map[string]interface{}{"id": dependentNodeID},
				Dependencies: []string{sourceNodeID},
			},
		},
		Rules: map[string]shared.Rule{
			ruleTrueID: {ID: ruleTrueID, Expression: "true"},
		},
	}
	input := DAGWorkflowInput{Config: config}

	// --- Attempt 1 Mocks ---
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["id"] == sourceNodeID && params["signal_for_attempt1_source"] == true
	})).Return("SourceOutput_Attempt1", nil).Once()

	// --- Attempt 2 Mocks (after reset due to validity timer) ---
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["id"] == sourceNodeID && params["signal_for_attempt2_source"] == true
	})).Return("SourceOutput_Attempt2", nil).Once()
	s.env.OnActivity("SimpleSyncActivity", mock.Anything, mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["id"] == dependentNodeID && params["signal_for_attempt2_dependent"] == true
	})).Return("DependentOutput_Attempt2", nil).Once()

	// Send signals using delayed callbacks instead of trying to advance time
	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(sourceSignal, map[string]interface{}{"signal_for_attempt1_source": true})
	}, 1*time.Millisecond)
	
	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(sourceSignal, map[string]interface{}{"signal_for_attempt2_source": true})
	}, 4*time.Second) // After validity timer expires
	
	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(dependentSignal, map[string]interface{}{"signal_for_attempt2_dependent": true})
	}, 5*time.Second)

	s.env.ExecuteWorkflow(DAGWorkflow, input)

	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())

	var results map[string]interface{}
	s.NoError(s.env.GetWorkflowResult(&results))
	s.Equal("SourceOutput_Attempt2", results[sourceNodeID])
	s.Equal("DependentOutput_Attempt2", results[dependentNodeID])
}
