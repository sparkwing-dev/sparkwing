package jobs

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Lint runs fast, repo-wide checks: gofmt compliance and go vet
// across every package in the sparkwing module. Mirrors the
// platform-repo `lint` pipeline so cross-repo callers (notably
// sparkwing-platform/release-all) can target the same name in
// either codebase.
type Lint struct{ sparkwing.Base }

func (Lint) ShortHelp() string { return "Fast static check: gofmt + go vet" }

func (Lint) Help() string {
	return "Fast static checks across the public sparkwing module: gofmt compliance and go vet."
}

func (Lint) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Quick static check before pushing a local change", Command: "wing lint"},
	}
}

func (p *Lint) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, sparkwing.JobFn(p.run))
	return nil
}

func (p *Lint) run(ctx context.Context) error {
	// `gofmt -l` exits 0 regardless and prints unformatted paths on
	// stdout; treat any output as a failure so lint does what its
	// name promises.
	if err := sparkwing.Bash(ctx, `gofmt -l $(go list -f '{{.Dir}}' ./...)`).
		MustBeEmpty("gofmt reported unformatted files"); err != nil {
		return err
	}
	sparkwing.Info(ctx, "gofmt: all files formatted")

	if _, err := sparkwing.Bash(ctx, "go vet ./...").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "go vet: no issues")
	return nil
}

func init() {
	sparkwing.Register("lint", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Lint{} })
}
