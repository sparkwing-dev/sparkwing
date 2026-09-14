//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const riskFixtureModule = "riskfixture"

const riskMarkerEnv = "RISK_FIXTURE_MARKER"

// TestRun_FirstRunInAFreshHomeRefusesADeclaredRisk runs a pipeline whose first
// step declares two risk labels, in a home that has never compiled it, and
// requires the refusal on that invocation. The labels live in Go source, so
// they are only legible once the .sparkwing/ binary exists; a dispatcher that
// reads them from what an earlier run left behind admits the first run of every
// fresh machine with no gate at all.
func TestRun_FirstRunInAFreshHomeRefusesADeclaredRisk(t *testing.T) {
	if testing.Short() {
		t.Skip("the risk gate compiles a fixture pipeline binary; run without -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	cli := buildSubmitCLI(t)

	sparkwingHome := t.TempDir()
	t.Setenv("SPARKWING_HOME", sparkwingHome)
	offlineStopDaemon(t, sparkwingHome)
	marker := filepath.Join(t.TempDir(), "ran.txt")
	repoDir, sparkwingDir := riskWriteFixture(t)

	env := append(os.Environ(),
		"SPARKWING_HOME="+sparkwingHome,
		"SPARKWING_LOG_FORMAT=quiet",
		"GOWORK=off",
		riskMarkerEnv+"="+marker,
	)
	if out, tidyErr := offlineRunGo(goBin, sparkwingDir, env, "mod", "tidy"); tidyErr != nil {
		t.Fatalf("resolving the fixture's modules: %v\n%s", tidyErr, out)
	}

	refused, err := offlineRunCLI(cli, repoDir, env, "run", "risky")
	if err == nil {
		t.Fatalf("a run with no --sw-allow was admitted:\n%s", refused)
	}
	if !strings.Contains(refused, "compiling .sparkwing/") {
		t.Fatalf("the refusal came from a home that had already compiled the pipeline, so it proves nothing:\n%s", refused)
	}
	for _, want := range []string{`step "push"`, "destructive", "prod", "--sw-allow"} {
		if !strings.Contains(refused, want) {
			t.Fatalf("the refusal does not name %q:\n%s", want, refused)
		}
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the refused run executed pipeline work: %v", statErr)
	}

	allowed, err := offlineRunCLI(cli, repoDir, env, "run", "risky", "--sw-allow", "destructive,prod")
	if err != nil {
		t.Fatalf("an authorized run was refused: %v\n%s", err, allowed)
	}
	if got := offlineMarkerLines(t, marker); len(got) != 2 || got[0] != "push" || got[1] != "after" {
		t.Fatalf("the authorized run wrote %q, want the risk-declaring step and the one that needs it", got)
	}
}

func riskWriteFixture(t *testing.T) (repoDir, sparkwingDir string) {
	t.Helper()
	repoDir = t.TempDir()
	sparkwingDir = filepath.Join(repoDir, ".sparkwing")
	if err := os.MkdirAll(filepath.Join(sparkwingDir, "jobs"), 0o755); err != nil {
		t.Fatalf("create %s: %v", sparkwingDir, err)
	}
	writeFile(t, filepath.Join(sparkwingDir, "go.mod"), fmt.Sprintf(
		"module %s\n\ngo 1.26.0\n\nrequire github.com/sparkwing-dev/sparkwing v0.0.0\n\n"+
			"replace github.com/sparkwing-dev/sparkwing => %s\n",
		riskFixtureModule, offlineRepoRoot(t)))
	writeFile(t, filepath.Join(sparkwingDir, "sparkwing.yaml"),
		"pipelines:\n  - name: risky\n    entrypoint: Risky\n    description: Declares a risk on its first step\n")
	writeFile(t, filepath.Join(sparkwingDir, "jobs", "jobs.go"), riskFixtureJobs)
	writeFile(t, filepath.Join(sparkwingDir, "main.go"), riskFixtureMain)
	return repoDir, sparkwingDir
}

const riskFixtureJobs = `package jobs

import (
	"context"
	"errors"
	"os"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Risky struct{ sparkwing.Base }

func (p *Risky) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	cut := sparkwing.Job(plan, "cut", &cutJob{})
	after := sparkwing.Job(plan, "after", func(ctx context.Context) error { return note("after") })
	after.Needs(cut)
	return nil
}

type cutJob struct{ sparkwing.Base }

func (j *cutJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "push", func(ctx context.Context) error { return note("push") }).
		Risk("destructive", "prod")
	return nil, nil
}

func note(what string) error {
	f, err := os.OpenFile(os.Getenv("RISK_FIXTURE_MARKER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(what + "\n"); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}

func init() {
	sparkwing.Register("risky", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Risky{} })
}
`

const riskFixtureMain = `package main

import (
	_ "riskfixture/jobs"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
)

func main() { runner.Main() }
`
