package jobs

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// publicBinaries is the canonical list of binaries the public
// sparkwing repo ships under cmd/. Mirrors the GH-Actions
// release matrix in .github/workflows/release.yaml; keep in sync
// when adding/removing a cmd entry.
var publicBinaries = []string{
	"sparkwing",
	"sparkwing-local-ws",
	"sparkwing-cache",
	"sparkwing-controller",
	"sparkwing-runner",
	"sparkwing-logs",
	"sparkwing-web",
}

// Build verifies every cmd/* binary in the public sparkwing repo
// compiles cleanly for the host platform. Sanity build only --
// production multi-arch + container builds are owned by the GH-
// Actions workflow at `.github/workflows/release.yaml`, which fires
// on tag push (the tag the `release` pipeline below pushes).
//
// This pipeline exists primarily as a cross-ref target for
// sparkwing-platform/release-all when an operator wants to gate the
// platform release on a known-good public build before tagging. It
// does NOT publish artifacts.
type Build struct{ sparkwing.Base }

func (Build) ShortHelp() string {
	return "Verify every cmd/* binary compiles (no publish)"
}

func (Build) Help() string {
	return "Runs `go build` for each binary under cmd/ on the host platform. Local-only sanity check; the production multi-arch + container builds are owned by `.github/workflows/release.yaml`, which fires on tag push."
}

func (Build) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Sanity-build every public binary", Command: "wing build"},
	}
}

func (p *Build) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	for _, bin := range publicBinaries {
		bin := bin
		sparkwing.Job(plan, "build-"+bin, sparkwing.JobFn(func(ctx context.Context) error {
			cmd := fmt.Sprintf("go build -o /dev/null ./cmd/%s", bin)
			if _, err := sparkwing.Bash(ctx, cmd).Run(); err != nil {
				return fmt.Errorf("build %s: %w", bin, err)
			}
			sparkwing.Info(ctx, "build %s: ok", bin)
			return nil
		}))
	}
	return nil
}

func init() {
	sparkwing.Register("build", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Build{} })
}
