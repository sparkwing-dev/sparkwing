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
	for _, fromTrigger := range []bool{true, false} {
		t.Run(map[bool]string{true: "detached", false: "foreground"}[fromTrigger], func(t *testing.T) {
			repo, cache, logger := cronPinnedFixture(t)
			repo, pipelineRevision := writeRetryTestRepo(t, repo, "https://example.test/acme/build.git", "selected-pipeline")
			selectedProgram := `package main
import "os"
func main() {
 body,err:=os.ReadFile("subject.txt"); if err != nil {panic(err)}
 if err:=os.WriteFile(os.Getenv("SPARKWING_RETRY_TEST_OUTPUT"),append([]byte("selected-pipeline:"),body...),0600);err!=nil{panic(err)}
}`
			for path, body := range map[string]string{
				".sparkwing/main.go":        selectedProgram,
				".sparkwing/sparkwing.yaml": "pipelines:\n  - name: pre-push\n    entrypoint: Fixture\n    source: selected\n",
				"subject.txt":               "main-subject",
			} {
				if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runGitForRetryTest(t, repo, "add", ".")
			runGitForRetryTest(t, repo, "commit", "-m", "selected pipeline reads subject")
			pipelineRevision = strings.TrimSpace(runGitForRetryTest(t, repo, "rev-parse", "HEAD"))
			runGitForRetryTest(t, repo, "branch", "selected")
			if err := os.WriteFile(filepath.Join(repo, "subject.txt"), []byte("branch-subject"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "main.go"), []byte("unbuildable caller pipeline"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitForRetryTest(t, repo, "commit", "-am", "change pipeline")
			revision := strings.TrimSpace(runGitForRetryTest(t, repo, "rev-parse", "HEAD"))
			runGitForRetryTest(t, repo, "branch", "-f", "selected", "HEAD")
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if fromTrigger {
				if err := st.CreateTrigger(t.Context(), store.Trigger{
					ID: "source", Pipeline: "pre-push", CreatedAt: time.Now(),
					TriggerEnv: map[string]string{PipelineRevKey: pipelineRevision},
				}); err != nil {
					t.Fatal(err)
				}
			}

			invocationRevision := pipelineRevision
			if fromTrigger {
				invocationRevision = strings.Repeat("d", 40)
			}
			if err := st.CreateRun(t.Context(), store.Run{
				ID: "source", Pipeline: "pre-push", Status: "failed", StartedAt: time.Now(),
				RepoURL: "https://example.test/acme/build.git", GitSHA: revision,
				PlanSnapshot: []byte("{}"), Invocation: map[string]any{"cwd": repo, "pipeline_revision": invocationRevision},
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
			if err != nil || string(got) != "selected-pipeline:branch-subject" {
				t.Fatalf("retry output = %q, %v; want selected-pipeline:branch-subject", got, err)
			}
			if after := runGitForRetryTest(t, repo, "worktree", "list", "--porcelain"); after != before {
				t.Fatalf("retry left worktrees behind:\n%s", after)
			}
		})
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

func TestDeclaredSourceLocalTriggerIgnoresCallerPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and executes selected trigger source")
	}
	repo, cache, logger := cronPinnedFixture(t)
	repo, revision := writeRetryTestRepo(t, repo, "", "selected source")
	runGitForRetryTest(t, repo, "branch", "selected")
	config := "pipelines:\n  - name: pre-push\n    entrypoint: Fixture\n    source: selected\n"
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "sparkwing.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "main.go"), []byte("caller cannot compile"), 0o600); err != nil {
		t.Fatal(err)
	}
	trigger := &store.Trigger{ID: "source-trigger", Pipeline: "pre-push", TriggerEnv: map[string]string{SubmitRepoDirKey: repo}}
	output := filepath.Join(t.TempDir(), "output")
	t.Setenv("SPARKWING_RETRY_TEST_OUTPUT", output)
	if err := dispatchLocalTrigger(t.Context(), trigger, "", "", cache, logger, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(output)
	if err != nil || string(body) != "selected source" {
		t.Fatalf("source execution: %q, %v", body, err)
	}
	if trigger.TriggerEnv[PipelineRevKey] != revision {
		t.Fatalf("source revision: %v", trigger.TriggerEnv)
	}
}
