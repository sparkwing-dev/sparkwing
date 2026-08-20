package jobs

import (
	"context"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Test runs `go test ./...` across the public sparkwing module.
// -race is omitted intentionally: this OSS lane runs the plain
// suite so it stays fast as a cross-repo gate; a heavier matrix
// (race + fuzz + integration) belongs in a downstream release-all
// pipeline.
type Test struct{ sparkwing.Base }

func (Test) ShortHelp() string { return "Run the Go test suite (go test ./...)" }

func (Test) Help() string {
	return "Runs the Go test suite for the public sparkwing module (`go test ./...`)."
}

func (Test) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run the full test suite", Command: "sparkwing run test"},
	}
}

func (p *Test) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(float64(preCommitCPUReservation(runtime.NumCPU()))))
	sparkwing.Job(plan, rc.Pipeline, p.run)
	return nil
}

func (p *Test) run(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, testGoCommand(runtime.NumCPU())).Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "go test: all packages passed")
	return nil
}

func testGoCommand(cpuCount int) string {
	return boundedGoCommand(cpuCount, "test", "./...")
}

func init() {
	sparkwing.Register("test", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Test{} })
}
