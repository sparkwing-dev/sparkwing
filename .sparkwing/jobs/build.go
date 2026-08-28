package jobs

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var publicBinaries = []string{
	"sparkwing",
	"sparkwing-cache",
	"sparkwing-controller",
	"sparkwing-runner",
	"sparkwing-logs",
	"sparkwing-web",
}

type Build struct{ sparkwing.Base }

func (Build) ShortHelp() string {
	return "Verify every shipped cmd/ binary compiles (no publish)"
}

func (Build) Help() string {
	return "Runs `go build` for each binary sparkwing ships from cmd/ (the same list as the GH-Actions release matrix; internal tools such as cmd/apidiff are not covered) on the host platform. Local-only sanity check; the production multi-arch + container builds are owned by `.github/workflows/release.yaml`, which fires on tag push."
}

func (Build) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Sanity-build every public binary", Command: "sparkwing run build"},
	}
}

func (p *Build) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	for _, bin := range publicBinaries {
		sparkwing.Job(plan, "build-"+bin, func(ctx context.Context) error {
			cmd := fmt.Sprintf("go build -o /dev/null ./cmd/%s", bin)
			if _, err := sparkwing.Bash(ctx, cmd).Run(); err != nil {
				return fmt.Errorf("build %s: %w", bin, err)
			}
			sparkwing.Info(ctx, "build %s: ok", bin)
			return nil
		})
	}
	return nil
}

func init() {
	sparkwing.Register("build", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Build{} })
}
