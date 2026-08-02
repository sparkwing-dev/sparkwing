package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestUnlocatableChildError_NamesRealCauseNotPhantomVerb(t *testing.T) {
	msg := unlocatableChildError("light").Error()

	if strings.Contains(msg, "pipeline add") {
		t.Fatalf("error recommends the phantom `sparkwing pipeline add` verb: %q", msg)
	}
	for _, want := range []string{
		"light",
		"no git identity",
		"sparkwing configure xrepo add",
		"WithFreshRepo",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q; got %q", want, msg)
		}
	}
}

func TestRepoDeclaresPipeline_FalseWithoutSparkwingDir(t *testing.T) {
	if repoDeclaresPipeline(t.TempDir(), "anything") {
		t.Fatal("a directory with no .sparkwing/ must not claim to declare a pipeline")
	}
}

func TestLocateTriggerRepo_RetryUsesRecordedCheckoutAcrossSameNamedPipelines(t *testing.T) {
	repoA := writeRetryTestRepo(t, "repo-a", "step-from-a")
	repoB := writeRetryTestRepo(t, "repo-b", "step-from-b")
	trig := &store.Trigger{
		Pipeline: "pre-push",
		Repo:     "owner/repo-a",
		RetryOf:  "source-run",
		TriggerEnv: map[string]string{
			retryprovenance.RepoDirKey:  repoA,
			retryprovenance.PlanHashKey: "sha256:source-plan",
		},
	}

	got, err := locateTriggerRepo(trig, repoB)
	if err != nil {
		t.Fatalf("locateTriggerRepo: %v", err)
	}
	want, err := filepath.EvalSymlinks(repoA)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("retry selected %q, want exact source checkout %q", got, want)
	}
	raw, err := os.ReadFile(filepath.Join(got, ".sparkwing", "sparkwing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "step-from-a") || strings.Contains(string(raw), "step-from-b") {
		t.Fatalf("selected retry manifest is not repo A: %s", raw)
	}
}

func TestLocateTriggerRepo_RetryFailsClosedWhenSourceCheckoutUnavailable(t *testing.T) {
	trig := &store.Trigger{Pipeline: "pre-push", Repo: "owner/repo-a", RetryOf: "source-run"}
	_, err := locateTriggerRepo(trig, writeRetryTestRepo(t, "repo-b", "step-from-b"))
	var unavailable *RetrySourceUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error=%T %v, want RetrySourceUnavailableError", err, err)
	}
	if !strings.Contains(err.Error(), "source run did not record") {
		t.Fatalf("unclear unavailable-worktree error: %v", err)
	}
}

func TestLocateTriggerRepo_RetryRejectsRepositoryIdentityDrift(t *testing.T) {
	repoB := writeRetryTestRepo(t, "repo-b", "step-from-b")
	trig := &store.Trigger{
		Pipeline: "pre-push",
		Repo:     "owner/repo-a",
		RetryOf:  "source-run",
		TriggerEnv: map[string]string{
			retryprovenance.RepoDirKey:  repoB,
			retryprovenance.PlanHashKey: "sha256:source-plan",
		},
	}
	_, err := locateTriggerRepo(trig, "")
	var unavailable *RetrySourceUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "identity drift") {
		t.Fatalf("error=%T %v, want typed repository identity drift", err, err)
	}
}

func writeRetryTestRepo(t *testing.T, name, step string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	for _, sub := range []string{".git", ".sparkwing"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "pipelines:\n  - name: pre-push\n    steps:\n      - " + step + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
