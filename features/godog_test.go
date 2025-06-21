package features

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/colors"

	// We will define step definitions in a separate package, e.g., "steps"
	"flow-shift/features/steps" // Actual steps package
)

var opts = godog.Options{
	Output: colors.Colored(os.Stdout),
	Format: "pretty", // Other formats: "progress", "junit"
	Paths:  []string{"."}, // Look for .feature files in the current directory (features/)
	Strict: true, // Fail if there are undefined or pending steps
	// Tags:   "", // To filter scenarios by tags
}

func init() {
	godog.BindCommandLineFlags("godog.", &opts) // Allow overriding options with flags
}

// TestMain runs the Godog test suite.
func TestMain(m *testing.M) {
	flag.Parse()
	opts.Paths = flag.Args() // Use arguments as paths if any provided

	status := godog.TestSuite{
		Name:                "godog",
		TestSuiteInitializer: InitializeTestSuite,
		ScenarioInitializer:  InitializeScenario, // Each scenario gets its own context
		Options:             &opts,
	}.Run()

	if st := m.Run(); st > status {
		status = st
	}
	os.Exit(status)
}

// InitializeTestSuite can be used for suite-level setup, like connecting to Temporal.
// However, for Temporal client, it might be better to initialize per scenario
// or have a shared client managed carefully if tests are parallel.
// For now, we'll manage client within scenario contexts.
func InitializeTestSuite(ctx *godog.TestSuiteContext) {
	// Example: If running tests in parallel, you might set up shared resources here.
	// ctx.BeforeSuite(func() { fmt.Println("Before test suite") })
	// ctx.AfterSuite(func() { fmt.Println("After test suite") })
}

// InitializeScenario registers step definitions for the scenarios.
func InitializeScenario(ctx *godog.ScenarioContext) {
	// Create a new context for each scenario to ensure isolation
	stepsCtx := steps.NewWorkflowTestContext()
	stepsCtx.RegisterSteps(ctx)
}

// Note:
// 1. Create a `steps` subdirectory: `mkdir features/steps`
// 2. Create `features/steps/dag_workflow_steps.go` for step definitions.
// 3. The `godog` binary is not directly run; `go test ./features/...` will execute these tests.
// 4. A Temporal server (e.g., dev server) must be running for these ITs.
// 5. `go mod tidy` will be needed to fetch `github.com/cucumber/godog`.
