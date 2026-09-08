package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

func TestRunsSubmit_PriorityRidesOnTheTriggerNotTheArgs(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	res := e.submit("--sw-priority", "front")
	trig, err := e.store().GetTrigger(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := trig.TriggerEnv[orchestrator.SubmitPriorityKey]; got != "front" {
		t.Fatalf("trigger priority = %q, want front (unresolved until the consumer launches it)", got)
	}
	for k := range trig.Args {
		if strings.HasPrefix(k, "sw-") {
			t.Fatalf("--sw-priority leaked into the pipeline args as %q", k)
		}
	}
}

// safety: a key names one intent, and asking for the same work in more of a
// hurry is that same intent, so the repeat must answer with the original run.
func TestRunsSubmit_PriorityDoesNotDeduplicate(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	first := e.submit("--idempotency-key", "priority-dedup", "--sw-priority", "3")
	second := e.submit("--idempotency-key", "priority-dedup", "--sw-priority", "back")
	if second.RunID != first.RunID {
		t.Fatalf("a differing priority started a second run %q, want the original %q",
			second.RunID, first.RunID)
	}
	if !second.AlreadySubmitted {
		t.Fatal("the repeat did not report already_submitted")
	}
}

func TestRunsSubmit_RefusesAnUnusablePriority(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	args := []string{
		"runs", "submit", "-o", "json", "--home", e.home, "-C", e.repoDir,
		"--sw-priority", "sideways", "fixture",
	}
	_, errOut, err := e.runStdout(args...)
	if err == nil {
		t.Fatal("an unusable --sw-priority was accepted")
	}
	if !strings.Contains(errOut, "--sw-priority") {
		t.Fatalf("the refusal does not name the flag: %s", errOut)
	}
}

const priorityFixtureSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--describe" {
		fmt.Print("[{\"name\":\"fixture\"}]")
		return
	}
	fmt.Println("observed-priority=" + os.Getenv("SPARKWING_PRIORITY"))
}
`

// safety: --sw-dry-run keeps the pre-warm from starting a daemon in the
// temporary home; the env plumbing under test happens either way.
func TestRun_PriorityReachesThePipelineProgramUnresolved(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the compile-and-exec path is exercised on POSIX process semantics")
	}
	bin := buildSubmitCLI(t)
	home, repoDir := t.TempDir(), t.TempDir()
	sparkwingDir := filepath.Join(repoDir, ".sparkwing")
	if err := os.MkdirAll(sparkwingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "go.mod"),
		[]byte("module priorityfixture\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "main.go"),
		[]byte(priorityFixtureSource), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "run", "fixture", "--sw-priority", "front", "--sw-dry-run")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_REPOS="+filepath.Join(home, "repos.yaml"),
		"SPARKWING_NO_UPDATE=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sparkwing run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "observed-priority=front") {
		t.Fatalf("pipeline program saw %q, want an unresolved front", out)
	}
}

func TestDispatchRun_RefusesAnUnusablePriorityBeforeAnythingElse(t *testing.T) {
	err := dispatchRun([]string{"no-such-pipeline", "--sw-priority", "sideways"})
	if err == nil {
		t.Fatal("an unusable --sw-priority was accepted")
	}
	if !strings.Contains(err.Error(), "--sw-priority") {
		t.Fatalf("dispatchRun = %v, want the flag named", err)
	}
}
