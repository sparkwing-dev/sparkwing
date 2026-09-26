package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runretry"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPipelineRefRetryRecreatesSourceAfterCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and executes a pipeline")
	}
	repo, cache, logger := cronPinnedFixture(t)
	repo, pipelineRevision := writeRetryTestRepo(t, repo, "https://example.test/acme/build.git", "selected-pipeline")
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "main.go"), []byte("unbuildable caller pipeline"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForRetryTest(t, repo, "commit", "-am", "change pipeline")
	revision := strings.TrimSpace(runGitForRetryTest(t, repo, "rev-parse", "HEAD"))
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateTrigger(t.Context(), store.Trigger{
		ID: "source", Pipeline: "pre-push", CreatedAt: time.Now(),
		TriggerEnv: map[string]string{PipelineRevKey: pipelineRevision},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(t.Context(), store.Run{
		ID: "source", Pipeline: "pre-push", Status: "failed", StartedAt: time.Now(),
		RepoURL: "https://example.test/acme/build.git", GitSHA: revision,
		PlanSnapshot: []byte("{}"), Invocation: map[string]any{"cwd": repo},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runretry.Create(t.Context(), st, "source", "retry", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	trigger, err := st.GetTrigger(t.Context(), "retry")
	if err != nil {
		t.Fatal(err)
	}
	if got := trigger.TriggerEnv[PipelineRevKey]; got != pipelineRevision {
		t.Fatalf("retry pipeline revision = %q, want %q", got, pipelineRevision)
	}
	trigger.TriggerEnv[PipelineDirKey] = filepath.Join(t.TempDir(), "removed-source")
	output := filepath.Join(t.TempDir(), "result")
	t.Setenv("SPARKWING_RETRY_TEST_OUTPUT", output)
	before := runGitForRetryTest(t, repo, "worktree", "list", "--porcelain")
	if err := dispatchLocalTrigger(t.Context(), trigger, "", "", cache, logger, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil || string(got) != "selected-pipeline" {
		t.Fatalf("retry output = %q, %v; want selected-pipeline", got, err)
	}
	if after := runGitForRetryTest(t, repo, "worktree", "list", "--porcelain"); after != before {
		t.Fatalf("retry left worktrees behind:\n%s", after)
	}
}

func TestPipelineRefSelectsSubmittedSourceDirectory(t *testing.T) {
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := submittedPipelineDir(&store.Trigger{TriggerEnv: map[string]string{
		PipelineRevKey: "abc123", PipelineDirKey: tree,
	}}, "/checkout/.sparkwing")
	if err != nil || got != filepath.Join(tree, ".sparkwing") {
		t.Fatalf("compile dir = %q (%v), want the pipeline ref's tree", got, err)
	}
	got, err = submittedPipelineDir(&store.Trigger{TriggerEnv: map[string]string{}}, "/checkout/.sparkwing")
	if err != nil || got != "/checkout/.sparkwing" {
		t.Fatalf("compile dir without a pipeline ref = %q (%v), want the run's own checkout", got, err)
	}
}

func TestPipelineRefDispatchRejectsMissingSource(t *testing.T) {
	repoDir, cache, logger := cronPinnedFixture(t)
	err := dispatchLocalTrigger(context.Background(), &store.Trigger{
		ID:       "run-build",
		Pipeline: "build",
		TriggerEnv: map[string]string{
			SubmitRepoDirKey: repoDir,
			PipelineRevKey:   "abc123",
			PipelineDirKey:   filepath.Join(t.TempDir(), "reclaimed"),
		},
	}, "", repoDir, cache, logger, nil)
	if err == nil || !strings.Contains(err.Error(), "--sw-pipeline-ref") {
		t.Fatalf("dispatch = %v, want a failure naming the missing pipeline tree", err)
	}
}
