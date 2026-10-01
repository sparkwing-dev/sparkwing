package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func pipelineRefRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	mark := func(word string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".sparkwing"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), []byte("pipelines: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "which"), []byte(word), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	mark("pipeline-main")
	git("add", ".")
	git("commit", "-q", "-m", "main")
	git("checkout", "-q", "-b", "feature")
	mark("pipeline-feature")
	git("commit", "-q", "-am", "change pipeline")
	return dir
}

func TestPipelineRefResolutionDoesNotBuildTheCallerPipeline(t *testing.T) {
	repo := pipelineRefRepo(t)
	got, _, err := resolveSubmitRepo(t.Context(), "build", repo, "main")
	if err != nil || got != repo {
		t.Fatalf("resolve checkout without pipeline source = %q, %v; want %q", got, err, repo)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "main.go"), []byte("not valid Go"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err = resolveSubmitRepo(t.Context(), "build", repo, "main")
	if err != nil || got != repo {
		t.Fatalf("resolve checkout without a buildable pipeline = %q, %v; want %q", got, err, repo)
	}
}

func TestPipelineRefRequiresExecutionProjectConfiguration(t *testing.T) {
	repo := pipelineRefRepo(t)
	if err := os.Remove(filepath.Join(repo, ".sparkwing", "sparkwing.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveSubmitRepo(t.Context(), "build", repo, "main"); err == nil {
		t.Fatal("submission accepted an execution checkout without project configuration")
	}
}

func TestConcurrentSubmissionRefusesDifferentPipelineSource(t *testing.T) {
	repo := pipelineRefRepo(t)
	paths, st := pipelineRefStore(t)
	sub := submission{Pipeline: "build", RepoDir: repo, PipelineRef: "main", IdempotencyKey: "race"}
	sub.Gate = func(string, string) error {
		other := sub
		other.PipelineRef = "feature"
		other.Gate = func(string, string) error { return nil }
		_, err := persistSubmission(t.Context(), st, paths, other)
		return err
	}
	_, err := persistSubmission(t.Context(), st, paths, sub)
	if err == nil || !strings.Contains(err.Error(), "compiled from a different tree") {
		t.Fatalf("conflicting submission = %v, want a pipeline-source mismatch", err)
	}
}

func pipelineRefStore(t *testing.T) (orchestrator.Paths, *store.Store) {
	t.Helper()
	paths := orchestrator.PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return paths, st
}

func TestPipelineRefSubmissionSeparatesSourceAndExecutionPaths(t *testing.T) {
	repo := pipelineRefRepo(t)
	paths, st := pipelineRefStore(t)
	var gated string
	result, err := persistSubmission(t.Context(), st, paths, submission{
		Pipeline: "build", RepoDir: repo, PipelineRef: "main",
		Gate: func(dir, _ string) error { gated = dir; return nil },
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	trig, err := st.GetTrigger(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}

	if got := trig.TriggerEnv[orchestrator.SubmitRepoDirKey]; got != repo {
		t.Errorf("the run executes in %q, want the caller's checkout %q", got, repo)
	}
	compileTree := trig.TriggerEnv[orchestrator.PipelineDirKey]
	which, err := os.ReadFile(filepath.Join(compileTree, ".sparkwing", "which"))
	if err != nil || string(which) != "pipeline-main" {
		t.Errorf("the pipeline compiles from %q holding %q (%v), want main's pipeline", compileTree, which, err)
	}
	if gated != compileTree {
		t.Errorf("the risk gate weighed %q, want the tree the pipeline compiles from %q", gated, compileTree)
	}
	if trig.TriggerEnv[orchestrator.RefWorktreeRevKey] != "" {
		t.Error("a pipeline ref recorded a run ref, so the run would move out of the caller's checkout")
	}
}

func TestSubmissionWithoutPipelineRefRecordsNoSourceOverride(t *testing.T) {
	repo := pipelineRefRepo(t)
	paths, st := pipelineRefStore(t)
	result, err := persistSubmission(t.Context(), st, paths, submission{
		Pipeline: "build", RepoDir: repo, Gate: func(string, string) error { return nil },
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	trig, err := st.GetTrigger(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if trig.TriggerEnv[orchestrator.PipelineRevKey] != "" || trig.TriggerEnv[orchestrator.PipelineDirKey] != "" {
		t.Errorf("a submission naming no pipeline ref recorded one: %v", trig.TriggerEnv)
	}
}

func TestRunRefAndPipelineRefConflict(t *testing.T) {
	repo := pipelineRefRepo(t)
	paths, st := pipelineRefStore(t)
	_, err := persistSubmission(t.Context(), st, paths, submission{
		Pipeline: "build", RepoDir: repo, Ref: "feature", PipelineRef: "main",
		Gate: func(string, string) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "--sw-pipeline-ref") {
		t.Fatalf("submission naming both refs = %v, want a refusal naming the pair", err)
	}
}

func TestPipelineRefResubmissionRejectsChangedSource(t *testing.T) {
	repo := pipelineRefRepo(t)
	paths, st := pipelineRefStore(t)
	submit := func(ref string) error {
		_, err := persistSubmission(t.Context(), st, paths, submission{
			Pipeline: "build", RepoDir: repo, PipelineRef: ref, IdempotencyKey: "build-once",
			Gate: func(string, string) error { return nil },
		})
		return err
	}
	if err := submit("main"); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if err := submit("feature"); err == nil || !strings.Contains(err.Error(), "compiled from a different tree") {
		t.Fatalf("resubmit with the pipeline at another commit = %v, want a refusal", err)
	}
}

func TestPipelineRefFlagAllowsForegroundRun(t *testing.T) {
	for _, args := range [][]string{{"--sw-pipeline-ref", "main"}, {"--sw-pipeline-ref=main"}} {
		flags, rest := parseRunFlags(args)
		if flags.pipelineRef != "main" || len(rest) != 0 {
			t.Errorf("parse %v = %q with %v left over, want main and nothing left", args, flags.pipelineRef, rest)
		}
		if err := refuseDetachedOnlyFlags(flags); err != nil {
			t.Errorf("a foreground run with %v = %v", args, err)
		}
	}
}

func TestPipelineRefSubmissionValidatesSelectedName(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: compiles the selected pipeline source")
	}
	repo := pipelineRefRepo(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	files := map[string]string{
		"go.mod": "module selectedfixture\n\ngo 1.22\n",
		"main.go": `package main
import "fmt"
func main() { fmt.Print("[{\"name\":\"selected\"}]") }
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(repo, ".sparkwing", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "declare pipeline")
	git("branch", "declared")
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "main.go"), []byte("unbuildable caller"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, st := pipelineRefStore(t)
	submit := func(name string) (submitResult, error) {
		gate := riskGate{Surface: detachedPath, Pipeline: name, SubmitDir: repo}
		return persistSubmission(t.Context(), st, paths, submission{Pipeline: name, RepoDir: repo, PipelineRef: "declared", Gate: gate.check})
	}
	if _, err := submit("absent"); err == nil || !strings.Contains(err.Error(), "declares a pipeline") {
		t.Fatalf("absent pipeline submission = %v, want a name refusal", err)
	}
	triggers, err := st.ListTriggers(t.Context(), store.TriggerFilter{})
	if err != nil || len(triggers) != 0 {
		t.Fatalf("rejected submission left triggers: %d, %v", len(triggers), err)
	}
	runs, err := st.ListRuns(t.Context(), store.RunFilter{})
	if err != nil || len(runs) != 0 {
		t.Fatalf("rejected submission left runs: %d, %v", len(runs), err)
	}
	for _, dir := range []string{paths.RefWorktreesDir(), filepath.Join(paths.Root, "submission-environments")} {
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("rejected submission left files in %s: %v", dir, entries)
		}
	}
	if _, err := submit("selected"); err != nil {
		t.Fatalf("selected source was not used: %v", err)
	}
}
