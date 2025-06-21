# Feature: Dynamic DAG Workflow End-to-End Tests
# This feature tests the overall behavior of the dynamic DAG workflow,
# including conditional pathing, user inputs, validity periods, and error handling.

Background:
  Given a Temporal dev server is running
  And I have a Temporal client connection

Scenario: Successful DAG execution with conditional pathing
  Given the workflow configuration is loaded from "config.yaml"
  And I override the activity "Initializer::SimpleSyncActivity" to return:
    """json
    { "result_code": 1 }
    """
  And I override the activity "ApprovalGate_A::SimpleSyncActivity" to return:
    """json
    { "approved": true }
    """
  When I start the "DAGWorkflow" with workflow ID "e2e-conditional-success"
  And I send the signal "ApprovalSignal_A" with payload "{}" to workflow "e2e-conditional-success"
  Then the workflow "e2e-conditional-success" should complete successfully
  And the result for node "Initializer" should not be nil
  And the result for node "ApprovalGate_A" should not be nil
  And the result for node "Task_Branch_A1_Actual" should not be nil
  And the result for node "ApprovalGate_B" should be nil
  And the result for node "Final_Step" should not be nil

Scenario: UserInput node waits for signal and uses payload
  Given the workflow configuration is loaded from "config.yaml" specifically for start nodes "UserInput_A_WithValidity"
  And I override the activity "UserInput_A_WithValidity::SimpleSyncActivity" to expect params containing:
    """json
    { "signal_key": "signal_value" }
    """
    And return "UserInput_A_Activity_Done"
  When I start the "DAGWorkflow" with workflow ID "e2e-user-input-signal"
  And I wait for 1 second # Allow workflow to reach the signal waiting point
  And I send the signal "Signal_A_Validity" with payload:
    """json
    { "signal_key": "signal_value" }
    """
    to workflow "e2e-user-input-signal"
  Then the workflow "e2e-user-input-signal" should complete successfully
  And the result for node "UserInput_A_WithValidity" should be "UserInput_A_Activity_Done"

Scenario: ResultValidity - Dependent completes in time, timer cancelled
  Given the workflow configuration is loaded from "config.yaml" specifically for start nodes "UserInput_A_WithValidity", "UserInput_B_DependentOnAValidity"
  # UserInput_A_WithValidity has ResultValiditySeconds: 15
  # UserInput_B_DependentOnAValidity depends on UserInput_A_WithValidity
  And I override the activity "UserInput_A_WithValidity::SimpleSyncActivity" to return "SourceOutput_InTime"
  And I override the activity "UserInput_B_DependentOnAValidity::SimpleSyncActivity" to return "DependentOutput_InTime"
  When I start the "DAGWorkflow" with workflow ID "e2e-validity-in-time"
  And I send the signal "Signal_A_Validity" to workflow "e2e-validity-in-time"
  And I wait for 1 second # Ensure A completes
  And I send the signal "Signal_B_Validity" to workflow "e2e-validity-in-time" # B completes before 15s validity of A expires
  Then the workflow "e2e-validity-in-time" should complete successfully within 10 seconds
  And the result for node "UserInput_A_WithValidity" should be "SourceOutput_InTime"
  And the result for node "UserInput_B_DependentOnAValidity" should be "DependentOutput_InTime"
  # And the logs for workflow "e2e-validity-in-time" should show "Cancelling validity timer" for "UserInput_A_WithValidity" (Requires log inspection or specific workflow event)

Scenario: ResultValidity - Timer fires, nodes are reset and redo successfully
  Given the workflow configuration is loaded from "config.yaml" specifically for start nodes "UserInput_A_WithValidity", "UserInput_B_DependentOnAValidity"
  # UserInput_A_WithValidity has ResultValiditySeconds: 15 (YAML has 15s, let's use a shorter one for test if possible, or advance time)
  # For this test, assume ResultValiditySeconds is very short, e.g., 2 seconds in a test-specific config or by advancing time.
  # We'll use time advancement in step definitions.
  And I mock activity "UserInput_A_WithValidity::SimpleSyncActivity" attempt 1 to return "SourceOutput_Attempt1"
  And I mock activity "UserInput_A_WithValidity::SimpleSyncActivity" attempt 2 to return "SourceOutput_Attempt2"
  And I mock activity "UserInput_B_DependentOnAValidity::SimpleSyncActivity" attempt 1 to return "DependentOutput_Attempt2" # Dependent's 1st successful run is after source's 2nd.
  When I start the "DAGWorkflow" with workflow ID "e2e-validity-timer-fires"
  And I send the signal "Signal_A_Validity" to workflow "e2e-validity-timer-fires" # Source completes (attempt 1)
  And I wait for workflow "e2e-validity-timer-fires" to process signal and advance time by 16 seconds # Trigger validity timer (15s + buffer)
  And I send the signal "Signal_A_Validity" to workflow "e2e-validity-timer-fires" # Source re-triggered (attempt 2)
  And I wait for 1 second
  And I send the signal "Signal_B_Validity" to workflow "e2e-validity-timer-fires" # Dependent now completes with fresh data from source attempt 2
  Then the workflow "e2e-validity-timer-fires" should complete successfully
  And the result for node "UserInput_A_WithValidity" should be "SourceOutput_Attempt2"
  And the result for node "UserInput_B_DependentOnAValidity" should be "DependentOutput_Attempt2"

Scenario: Node Skipping
  Given the workflow configuration is loaded from "config.yaml" specifically for start nodes "Independent_Skipped_Node_Trigger"
  When I start the "DAGWorkflow" with workflow ID "e2e-node-skip"
  Then the workflow "e2e-node-skip" should complete successfully
  And the result for node "Independent_Skipped_Node_Trigger" should not be nil
  And the result for node "Actual_Skipped_Node" should be "SKIPPED"
  And the result for node "Final_Step" should not be nil # Assuming Actual_Skipped_Node targets Final_Step

Scenario: Node failure with redo
  Given the workflow configuration is loaded from "config.yaml" specifically for start nodes "Failing_Node_With_Redo"
  # Failing_Node_With_Redo uses ActivityThatCanFail, which fails on first 2 attempts if forceFail=true
  And I override the activity "Failing_Node_With_Redo::ActivityThatCanFail" to fail 2 times then succeed with "RedoSuccessOutput"
  When I start the "DAGWorkflow" with workflow ID "e2e-node-redo"
  Then the workflow "e2e-node-redo" should complete successfully
  And the result for node "Failing_Node_With_Redo" should be "RedoSuccessOutput"

Scenario: Activity Timeout
  Given a workflow configuration with a node "TimeoutTestNode" having:
    | property               | value                  |
    | activityName           | LongRunningActivity    |
    | activityTimeoutSeconds | 1                      |
    | nextNodeRules          | true -> DependentAfterTimeout |
  And node "DependentAfterTimeout" which depends on "TimeoutTestNode" and runs "SimpleSyncActivity"
  And I mock activity "LongRunningActivity" to simulate a StartToClose timeout
  And I mock activity "SimpleSyncActivity" to return "DependentRanAfterTimeout"
  When I start the "DAGWorkflow" with this configuration and ID "e2e-activity-timeout"
  Then the workflow "e2e-activity-timeout" should complete with an error containing "node TimeoutTestNode timedout"
  And the result for node "TimeoutTestNode" should be nil
  And the result for node "DependentAfterTimeout" should be "DependentRanAfterTimeout"

Scenario: Node Expiry (before activity start)
  Given a workflow configuration with nodes "A", "ExpireNode", "C" where "A" -> "ExpireNode" -> "C"
  And "ExpireNode" has ExpirySeconds of 1
  And I mock activity "A::ShortActivity" to complete quickly
  And I mock activity "C::SimpleSyncActivity" to return "C_Output"
  When I start the "DAGWorkflow" with this configuration and ID "e2e-node-expiry"
  And I wait for workflow "e2e-node-expiry" to process "A" and advance time by 2 seconds
  Then the workflow "e2e-node-expiry" should complete successfully
  And the result for node "ExpireNode" should be nil # Or specific "EXPIRED" marker if set by workflow
  And the result for node "C" should be "C_Output"
  And activity "ExpireNode::NeverRunActivity" should not have been called
