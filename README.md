# FlowShift

A dynamic Directed Acyclic Graph (DAG) workflow engine built on [Temporal](https://temporal.io/), enabling flexible, configurable workflow orchestration with YAML-based definitions.

## 🚀 Features

- **Dynamic DAG Workflows**: Define complex workflow patterns using YAML configuration
- **Conditional Routing**: Rule-based node transitions with custom expressions
- **Multiple Node Types**: Support for sync, async, wait, and user input activities
- **Retry Logic**: Configurable retry conditions for failed activities
- **Node Skipping**: Conditional node execution based on parameters or outputs
- **Result Validity**: Time-based result expiration for dependent nodes
- **User Input Handling**: Signal-based user interaction points
- **Comprehensive Testing**: Extensive test suite with Temporal's testing framework
- **Structured Logging**: Detailed logging with Zap logger integration

## 📋 Prerequisites

- Go 1.23.0 or higher
- Temporal Server (local development server or cloud instance)
- Git

## 🛠️ Installation

1. **Clone the repository**:
   ```bash
   git clone <repository-url>
   cd FlowShift
   ```

2. **Install dependencies**:
   ```bash
   go mod tidy
   ```

3. **Start Temporal Server** (for local development):
   ```bash
   temporal server start-dev --ui-port 8080
   ```

## 🏃‍♂️ Quick Start

### 1. Start the Worker and Workflow

Run both worker and workflow starter in combined mode:
```bash
go run main.go
```

### 2. Run in Separate Modes

**Worker only** (processes workflow tasks):
```bash
RUN_MODE=WORKER go run main.go
```

**Starter only** (triggers workflow execution):
```bash
RUN_MODE=STARTER go run main.go
```

### 3. Monitor Execution

Access the Temporal UI at `http://localhost:8080` to monitor workflow execution, view logs, and inspect workflow history.

## 📁 Project Structure

```
FlowShift/
├── activities/           # Activity implementations
│   └── sample_activities.go
├── features/            # BDD test specifications
│   ├── dynamic_dag_workflow.feature
│   ├── godog_test.go
│   └── steps/
│       └── dag_workflow_steps.go
├── shared/              # Shared types and utilities
│   └── types.go
├── workflow/            # Workflow implementation
│   ├── dag_workflow_test.go
│   ├── condition_evaluator.go
│   └── config_loader.go
├── config.yaml          # Sample workflow configuration
├── rules.yaml           # Reusable rule definitions
├── main.go              # Application entry point
└── go.mod               # Go module definition
```

## ⚙️ Configuration

### Workflow Configuration (`config.yaml`)

Define your workflow structure using YAML:

```yaml
startNodeIDs:
  - Initializer
  - Parallel_Task_1

nodes:
  Initializer:
    id: Initializer
    type: sync
    activityName: SimpleSyncActivity
    params: 
      message: "Workflow Initialized"
      initial_branch_choice: 1
    activityTimeoutSeconds: 60
    nextNodeRules:
      - { ruleId: IsResultCode1, targetNodeId: ApprovalGate_A }
      - { ruleId: IsResultCode2, targetNodeId: ApprovalGate_B }
      - { ruleId: AlwaysTrue, targetNodeId: Final_Step }

  ApprovalGate_A:
    id: ApprovalGate_A
    type: user_input
    activityName: SimpleSyncActivity
    params: 
      message: "Approval A path pending"
      approval_type: "TypeA"
    activityTimeoutSeconds: 300
    signalName: "ApprovalSignal_A"
    dependencies: [Initializer]
    nextNodeRules:
      - { ruleId: IsApproved, targetNodeId: Task_Branch_A1_Actual }
      - { ruleId: IsRejected, targetNodeId: Escalation_Step }
```

### Rules Configuration (`rules.yaml`)

Define reusable conditional expressions:

```yaml
rules:
  IsResultCode1:
    id: IsResultCode1
    expression: "output.result_code == 1"
    description: "Checks if the activity output contains result_code with value 1"

  IsApproved:
    id: IsApproved
    expression: "output.approved == true"
    description: "Checks if the activity output contains approved with boolean value true"

  AlwaysTrue:
    id: AlwaysTrue
    expression: "true"
    description: "An unconditional rule that always evaluates to true"
```

## 🔧 Node Types

### Sync Nodes
- **Type**: `sync`
- **Behavior**: Synchronous execution, waits for completion
- **Use Case**: Simple operations, data processing

### Async Nodes
- **Type**: `async`
- **Behavior**: Asynchronous execution, doesn't block workflow
- **Use Case**: Long-running operations, external API calls

### Wait Nodes
- **Type**: `wait`
- **Behavior**: Waits for a specified duration or condition
- **Use Case**: Polling, time-based delays, external event waiting

### User Input Nodes
- **Type**: `user_input`
- **Behavior**: Waits for external signal input
- **Use Case**: Human approval gates, manual intervention points

## 🧪 Testing

### Unit Tests
Run the comprehensive test suite:
```bash
go test ./...
```

### BDD Tests
Run behavior-driven development tests:
```bash
go test ./features/...
```

### Test Coverage
```bash
go test -cover ./...
```

## 🔍 Key Concepts

### Node Dependencies
Nodes can depend on other nodes using the `dependencies` field:
```yaml
Task_B:
  dependencies: [Task_A]  # Task_B will only start after Task_A completes
```

### Conditional Routing
Use `nextNodeRules` to define conditional paths:
```yaml
nextNodeRules:
  - { ruleId: IsSuccess, targetNodeId: SuccessPath }
  - { ruleId: IsFailure, targetNodeId: FailurePath }
```

### Retry Logic
Configure automatic retries for failed activities:
```yaml
redoCondition: "on_failure"  # Retry on any failure
```

### Node Skipping
Skip nodes based on conditions:
```yaml
skipCondition: "params._skip == true"
```

### Result Validity
Set time limits for result validity:
```yaml
resultValiditySeconds: 300  # Result valid for 5 minutes
```

## 🚨 Error Handling

- **Activity Timeouts**: Configurable per-node timeout limits
- **Node Expiry**: Nodes can expire before execution starts
- **Retry Logic**: Automatic retry with configurable conditions
- **Graceful Degradation**: Failed nodes don't necessarily stop the entire workflow

## 📊 Monitoring and Observability

- **Temporal UI**: Visual workflow monitoring and debugging
- **Structured Logging**: Detailed logs with Zap logger
- **Workflow History**: Complete execution history and state tracking
- **Metrics**: Built-in Temporal metrics and monitoring

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🆘 Support

- **Issues**: Report bugs and feature requests via GitHub Issues
- **Documentation**: Check the inline code documentation and examples
- **Temporal Community**: Join the [Temporal Slack](https://temporal.io/slack) for broader support

## 🔗 Related Links

- [Temporal Documentation](https://docs.temporal.io/)
- [Temporal Go SDK](https://docs.temporal.io/dev-guide/go)
- [YAML Specification](https://yaml.org/spec/)
- [Go Modules](https://golang.org/ref/mod)