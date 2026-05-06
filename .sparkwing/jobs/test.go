package jobs

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Test runs `go test ./...` across the public sparkwing module.
// -race is omitted intentionally: the SDK's race-clean test pass
// lives in the platform-repo `test` pipeline (where a heavier matrix
// is acceptable). The OSS pipeline runs the plain suite so cross-repo
// validation from sparkwing-platform/release-all stays fast.
type Test struct{ sparkwing.Base }

func (Test) ShortHelp() string { return "Run the Go test suite (go test ./...)" }

func (Test) Help() string {
	return "Runs the Go test suite for the public sparkwing module (`go test ./...`)."
}

func (Test) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run the full test suite", Command: "wing test"},
	}
}

func (p *Test) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, p.run)
	return nil
}

func (p *Test) run(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "go test ./...").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "go test: all packages passed")
	return nil
}

func init() {
	sparkwing.Register("test", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Test{} })
}
