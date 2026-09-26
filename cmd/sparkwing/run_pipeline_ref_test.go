package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
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
		t.Fatalf("resolve checkout without a buildable pipeline = %q, %v; want %q", got, err, repo)
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
	st, err := store.Open(paths.StateDB())
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

func TestPipelineRefFlagRequiresDetachedRun(t *testing.T) {
	for _, args := range [][]string{{"--sw-pipeline-ref", "main"}, {"--sw-pipeline-ref=main"}} {
		flags, rest := parseRunFlags(args)
		if flags.pipelineRef != "main" || len(rest) != 0 {
			t.Errorf("parse %v = %q with %v left over, want main and nothing left", args, flags.pipelineRef, rest)
		}
		if err := refuseDetachedOnlyFlags(flags); err == nil || !strings.Contains(err.Error(), "--sw-pipeline-ref") {
			t.Errorf("a foreground run with %v = %v, want the detached-only refusal", args, err)
		}
	}
}
