//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const riskFixtureModule = "riskfixture"

const riskMarkerEnv = "RISK_FIXTURE_MARKER"

// safety: the empty string writes the same fixture pipeline declaring nothing.
const riskDeclaration = ".\n\t\tRisk(\"destructive\", \"prod\")"

func TestPersistSubmissionRefusesDeclaredRiskBeforeQueuing(t *testing.T) {
	paths := orchestrator.PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	repoDir := t.TempDir()
	gate := riskGate{
		Surface: "detached", Pipeline: "risky", SubmitDir: repoDir,
		Declared: []sparkwing.DescribePipeline{{
			Name: "risky",
			RisksBySteps: []sparkwing.DescribeStepRisks{{
				NodeID: "cut", StepID: "push", Labels: []string{"destructive", "prod"},
			}},
		}},
	}
	_, err = persistSubmission(t.Context(), st, paths, submission{
		Pipeline: "risky", RepoDir: repoDir, Gate: gate.check,
	})
	var blocked *sparkwing.RiskBlockedError
	if !errors.As(err, &blocked) || blocked.StepID != "push" ||
		strings.Join(blocked.MissingLabels, ",") != "destructive,prod" {
		t.Fatalf("submission refusal = %v, want the step and both unapproved labels", err)
	}
	triggers, err := st.ListTriggers(t.Context(), store.TriggerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(triggers) != 0 {
		t.Fatalf("refused submission persisted %d triggers", len(triggers))
	}
}

// TestRun_FirstRunInAFreshHomeRefusesADeclaredRisk requires the refusal on the
// invocation that compiles the pipeline, not only on the one after it.
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
	repoDir, sparkwingDir := riskWriteFixture(t, riskDeclaration)

	env := append(os.Environ(),
		"SPARKWING_HOME="+sparkwingHome,
		"SPARKWING_LOG_FORMAT=quiet",
		"GOWORK=off",
		riskMarkerEnv+"="+marker,
	)
	if out, tidyErr := offlineRunTool(goBin, sparkwingDir, env, "mod", "tidy"); tidyErr != nil {
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

// TestRunDetached_RefusesADeclaredRisk requires the refusal at submission: a
// detached launch reaches the consumer with no operator attached and no allow
// on the trigger, so admitting one would run the risk-declaring step
// authorized by nothing.
func TestRunDetached_RefusesADeclaredRisk(t *testing.T) {
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
	repoDir, sparkwingDir := riskWriteFixture(t, riskDeclaration)

	env := append(os.Environ(),
		"SPARKWING_HOME="+sparkwingHome,
		"SPARKWING_LOG_FORMAT=quiet",
		"GOWORK=off",
		riskMarkerEnv+"="+marker,
	)
	if out, tidyErr := offlineRunTool(goBin, sparkwingDir, env, "mod", "tidy"); tidyErr != nil {
		t.Fatalf("resolving the fixture's modules: %v\n%s", tidyErr, out)
	}

	refused, err := offlineRunCLI(cli, repoDir, env, "run", "risky", "--sw-detached")
	if err == nil {
		t.Fatalf("a detached launch with no --sw-allow was admitted:\n%s", refused)
	}
	for _, want := range []string{`step "push"`, "destructive", "prod", "--sw-allow", "detached"} {
		if !strings.Contains(refused, want) {
			t.Fatalf("the refusal does not name %q:\n%s", want, refused)
		}
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the refused launch executed pipeline work: %v", statErr)
	}
}

// TestRunDetached_RefusesARiskTheRefDeclares requires the gate to weigh the
// checkout the run will execute. The working tree declares nothing and the ref
// declares two labels, so a gate that read the submitting tree would queue the
// run and the consumer would execute the risk-labeled step.
func TestRunDetached_RefusesARiskTheRefDeclares(t *testing.T) {
	if testing.Short() {
		t.Skip("the risk gate compiles a fixture pipeline binary; run without -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	cli := buildSubmitCLI(t)

	sparkwingHome := t.TempDir()
	t.Setenv("SPARKWING_HOME", sparkwingHome)
	offlineStopDaemon(t, sparkwingHome)
	marker := filepath.Join(t.TempDir(), "ran.txt")
	repoDir, sparkwingDir := riskWriteFixture(t, "")

	env := append(os.Environ(),
		"SPARKWING_HOME="+sparkwingHome,
		"SPARKWING_LOG_FORMAT=quiet",
		"GOWORK=off",
		riskMarkerEnv+"="+marker,
	)
	if out, tidyErr := offlineRunTool(goBin, sparkwingDir, env, "mod", "tidy"); tidyErr != nil {
		t.Fatalf("resolving the fixture's modules: %v\n%s", tidyErr, out)
	}

	git := func(args ...string) {
		t.Helper()
		out, gerr := offlineRunTool(gitBin, repoDir, riskGitEnv(t, env), args...)
		if gerr != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), gerr, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "a pipeline that declares nothing")
	git("checkout", "-q", "-b", "riskybranch")
	writeFile(t, filepath.Join(sparkwingDir, "jobs", "jobs.go"), fmt.Sprintf(riskFixtureJobs, riskDeclaration))
	git("commit", "-qam", "the same pipeline declaring a risk")
	git("checkout", "-q", "main")

	refused, err := offlineRunCLI(cli, repoDir, env, "run", "risky", "--sw-detached", "--sw-ref", "riskybranch")
	if err == nil {
		t.Fatalf("a detached launch at a risk-declaring ref was admitted:\n%s", refused)
	}
	for _, want := range []string{`step "push"`, "destructive", "prod", "--sw-allow"} {
		if !strings.Contains(refused, want) {
			t.Fatalf("the refusal does not name %q:\n%s", want, refused)
		}
	}
	if pid, ok := orchestrator.ConsumerPID(sparkwingHome); ok {
		t.Fatalf("the refused launch started consumer process %d", pid)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the refused launch executed pipeline work: %v", statErr)
	}
}

// TestCronLaunch_RefusesADeclaredRisk requires a scheduled launch to be weighed
// too: it queues a run through the same submission path with no operator and no
// allow.
func TestCronLaunch_RefusesADeclaredRisk(t *testing.T) {
	if testing.Short() {
		t.Skip("the risk gate compiles a fixture pipeline binary; run without -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}

	sparkwingHome := t.TempDir()
	t.Setenv("SPARKWING_HOME", sparkwingHome)
	marker := filepath.Join(t.TempDir(), "ran.txt")
	t.Setenv(riskMarkerEnv, marker)
	repoDir, sparkwingDir := riskWriteFixture(t, riskDeclaration)
	if out, tidyErr := offlineRunTool(goBin, sparkwingDir, append(os.Environ(), "GOWORK=off"), "mod", "tidy"); tidyErr != nil {
		t.Fatalf("resolving the fixture's modules: %v\n%s", tidyErr, out)
	}

	paths := orchestrator.PathsAt(sparkwingHome)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure %s: %v", paths.Root, err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open %s: %v", paths.StateDB(), err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close %s: %v", paths.StateDB(), cerr)
		}
	})

	_, err = cronLauncher{store: st, paths: paths}.Launch(context.Background(),
		store.CronSchedule{ID: "sched-risky", Pipeline: "risky", RepoPath: repoDir}, time.Now())
	if err == nil {
		t.Fatal("a scheduled launch of a risk-declaring pipeline was admitted")
	}
	for _, want := range []string{"sched-risky", `step "push"`, "destructive", "prod"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the refused schedule executed pipeline work: %v", statErr)
	}
}

// TestCronLaunch_WeighsThePinnedBinary requires the gate to weigh what an armed
// schedule will exec. Arming keeps the binary it compiled, so a checkout edited
// afterwards is not what runs, and its declarations are not the ones that count.
func TestCronLaunch_WeighsThePinnedBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("the risk gate compiles a fixture pipeline binary; run without -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}

	sparkwingHome := t.TempDir()
	t.Setenv("SPARKWING_HOME", sparkwingHome)
	marker := filepath.Join(t.TempDir(), "ran.txt")
	t.Setenv(riskMarkerEnv, marker)
	repoDir, sparkwingDir := riskWriteFixture(t, riskDeclaration)
	env := append(os.Environ(), "GOWORK=off", riskMarkerEnv+"="+marker)
	if out, tidyErr := offlineRunTool(goBin, sparkwingDir, env, "mod", "tidy"); tidyErr != nil {
		t.Fatalf("resolving the fixture's modules: %v\n%s", tidyErr, out)
	}
	pinned := filepath.Join(t.TempDir(), "pinned")
	if out, buildErr := offlineRunTool(goBin, sparkwingDir, env, "build", "-o", pinned, "."); buildErr != nil {
		t.Fatalf("building the pinned binary: %v\n%s", buildErr, out)
	}
	writeFile(t, filepath.Join(sparkwingDir, "jobs", "jobs.go"), fmt.Sprintf(riskFixtureJobs, ""))

	paths := orchestrator.PathsAt(sparkwingHome)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure %s: %v", paths.Root, err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open %s: %v", paths.StateDB(), err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close %s: %v", paths.StateDB(), cerr)
		}
	})

	_, err = cronLauncher{store: st, paths: paths}.Launch(context.Background(),
		store.CronSchedule{ID: "sched-pinned", Pipeline: "risky", RepoPath: repoDir, LockedBinary: pinned},
		time.Now())
	if err == nil {
		t.Fatal("a schedule pinned to a risk-declaring build was admitted because its checkout declares nothing")
	}
	if !strings.Contains(err.Error(), `step "push"`) {
		t.Fatalf("the refusal does not name the pinned build's step: %v", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the refused schedule executed pipeline work: %v", statErr)
	}
}

// safety: git reads the operator's identity and hooks from their home, which a
// fixture commit must not depend on.
func riskGitEnv(t *testing.T, env []string) []string {
	t.Helper()
	return append(append([]string(nil), env...),
		"HOME="+t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=sparkwing test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=sparkwing test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	)
}

// TestRun_RefusesASourceTreeItCannotRead requires a refusal, never an
// admission, when .sparkwing/ holds a file the dispatcher cannot read. The
// unreadable file is one the Go toolchain ignores, so the pipeline would still
// build and run: a tree that cannot be weighed is a tree whose labels are
// unknown.
func TestRun_RefusesASourceTreeItCannotRead(t *testing.T) {
	if testing.Short() {
		t.Skip("the unreadable-tree refusal builds a fixture pipeline; run without -short")
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
	repoDir, sparkwingDir := riskWriteFixture(t, riskDeclaration)

	env := append(os.Environ(),
		"SPARKWING_HOME="+sparkwingHome,
		"SPARKWING_LOG_FORMAT=quiet",
		"GOWORK=off",
		riskMarkerEnv+"="+marker,
	)
	if out, tidyErr := offlineRunTool(goBin, sparkwingDir, env, "mod", "tidy"); tidyErr != nil {
		t.Fatalf("resolving the fixture's modules: %v\n%s", tidyErr, out)
	}
	if err := os.Symlink("nowhere-at-all", filepath.Join(sparkwingDir, "notes.txt")); err != nil {
		t.Fatalf("place an unreadable file under %s: %v", sparkwingDir, err)
	}

	out, err := offlineRunCLI(cli, repoDir, env, "run", "risky")
	if err == nil {
		t.Fatalf("a tree the dispatcher cannot read was admitted:\n%s", out)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the refused run executed pipeline work: %v", statErr)
	}
}

func riskWriteFixture(t *testing.T, declaration string) (repoDir, sparkwingDir string) {
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
	writeFile(t, filepath.Join(sparkwingDir, "jobs", "jobs.go"), fmt.Sprintf(riskFixtureJobs, declaration))
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
	sparkwing.Step(w, "push", func(ctx context.Context) error { return note("push") })%s
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
