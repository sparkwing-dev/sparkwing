package jobs

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// StaticAnalysis runs the heavier static-analysis pass that doesn't
// fit in the fast `lint` pipeline: staticcheck plus a `go mod tidy
// -diff` drift check. govulncheck is intentionally omitted here --
// the platform-repo equivalent runs it, and re-running it in the OSS
// repo would double-bill on every release-all call without adding
// signal.
//
// staticcheck is invoked via `go run honnef.co/go/tools/cmd/staticcheck`
// so dev machines without a global install still pass; failure is
// reported as a soft skip with a clear message, since pinning a
// staticcheck version into go.sum would force every consumer of the
// sparkwing module to also pull it.
type StaticAnalysis struct{ sparkwing.Base }

func (StaticAnalysis) ShortHelp() string {
	return "Heavier static checks: staticcheck + tidy drift"
}

func (StaticAnalysis) Help() string {
	return "Runs staticcheck (via `go run`) and `go mod tidy -diff` against the public sparkwing module. Slower than `lint`; intended as a release gate."
}

func (StaticAnalysis) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Heavy static-analysis pass before cutting a release", Command: "wing static-analysis"},
	}
}

func (p *StaticAnalysis) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "staticcheck", p.staticcheck)
	sparkwing.Job(plan, "tidy-drift", p.tidyDrift)
	return nil
}

func (p *StaticAnalysis) staticcheck(ctx context.Context) error {
	// `go run` ensures the tool is available without a separate
	// install step; the module proxy caches it after first use.
	if _, err := sparkwing.Bash(ctx, "go run honnef.co/go/tools/cmd/staticcheck@latest ./...").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "staticcheck: no issues")
	return nil
}

func (p *StaticAnalysis) tidyDrift(ctx context.Context) error {
	if err := sparkwing.Bash(ctx, "go mod tidy -diff").
		MustBeEmpty("go.mod / go.sum drift detected; run `go mod tidy`"); err != nil {
		return err
	}
	sparkwing.Info(ctx, "go mod tidy: no drift")
	return nil
}

func init() {
	sparkwing.Register("static-analysis", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &StaticAnalysis{} })
}
