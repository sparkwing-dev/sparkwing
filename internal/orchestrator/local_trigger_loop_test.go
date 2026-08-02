package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
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
	repoA, shaA := writeRetryTestRepo(t, filepath.Join(t.TempDir(), "repo-a"), "git@example.test:owner/repo-a.git", "step-from-a")
	repoB, _ := writeRetryTestRepo(t, filepath.Join(t.TempDir(), "repo-b"), "git@example.test:owner/repo-b.git", "step-from-b")
	trig := &store.Trigger{
		Pipeline: "pre-push",
		Repo:     "owner/repo-a",
		RetryOf:  "source-run",
		TriggerEnv: map[string]string{
			retryprovenance.RepoDirKey:      repoA,
			retryprovenance.RepoIdentityKey: "git@example.test:owner/repo-a.git",
			retryprovenance.RevisionKey:     shaA,
			retryprovenance.PlanHashKey:     "sha256:source-plan",
		},
	}

	got, err := locateTriggerRepo(context.Background(), trig, repoB)
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
	raw, err := os.ReadFile(filepath.Join(got, ".sparkwing", "behavior.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "step-from-a" {
		t.Fatalf("selected retry behavior is not repo A: %s", raw)
	}
}

func TestLocateTriggerRepo_RetryFailsClosedWhenSourceCheckoutUnavailable(t *testing.T) {
	trig := &store.Trigger{Pipeline: "pre-push", Repo: "owner/repo-a", RetryOf: "source-run"}
	repoB, _ := writeRetryTestRepo(t, filepath.Join(t.TempDir(), "repo-b"), "git@example.test:owner/repo-b.git", "step-from-b")
	_, err := locateTriggerRepo(context.Background(), trig, repoB)
	var unavailable *RetrySourceUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error=%T %v, want RetrySourceUnavailableError", err, err)
	}
	if !strings.Contains(err.Error(), "source run did not record") {
		t.Fatalf("unclear unavailable-worktree error: %v", err)
	}
}

func TestLocateTriggerRepo_RetryRejectsRepositoryIdentityDrift(t *testing.T) {
	repoB, shaB := writeRetryTestRepo(t, filepath.Join(t.TempDir(), "repo-b"), "git@example.test:owner/repo-b.git", "step-from-b")
	trig := &store.Trigger{
		Pipeline: "pre-push",
		Repo:     "owner/repo-a",
		RetryOf:  "source-run",
		TriggerEnv: map[string]string{
			retryprovenance.RepoDirKey:      repoB,
			retryprovenance.RepoIdentityKey: "git@example.test:owner/repo-a.git",
			retryprovenance.RevisionKey:     shaB,
			retryprovenance.PlanHashKey:     "sha256:source-plan",
		},
	}
	_, err := locateTriggerRepo(context.Background(), trig, "")
	var unavailable *RetrySourceUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "identity drift") {
		t.Fatalf("error=%T %v, want typed repository identity drift", err, err)
	}
}

func TestLocateTriggerRepo_RetryRejectsSamePathSameBasenameReplacement(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "shared")
	_, shaA := writeRetryTestRepo(t, repoDir, "git@example.test:owner-a/shared.git", "behavior-a")
	if err := os.RemoveAll(repoDir); err != nil {
		t.Fatal(err)
	}
	replacement, _ := writeRetryTestRepo(t, repoDir, "git@example.test:owner-b/shared.git", "behavior-b")
	trig := &store.Trigger{
		Pipeline: "pre-push",
		Repo:     "shared",
		RetryOf:  "source-run",
		TriggerEnv: map[string]string{
			retryprovenance.RepoDirKey:      repoDir,
			retryprovenance.RepoIdentityKey: "git@example.test:owner-a/shared.git",
			retryprovenance.RevisionKey:     shaA,
			retryprovenance.PlanHashKey:     "sha256:matching-plan-shape",
		},
	}

	_, err := locateTriggerRepo(context.Background(), trig, "")
	var unavailable *RetrySourceUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "identity drift") {
		t.Fatalf("error=%T %v, want typed repository identity drift", err, err)
	}
	raw, readErr := os.ReadFile(filepath.Join(replacement, ".sparkwing", "behavior.txt"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != "behavior-b" {
		t.Fatalf("replacement behavior=%q, want behavior-b", raw)
	}
}

func TestLocateTriggerRepo_RetryRejectsRevisionDriftBeforeCompilation(t *testing.T) {
	repoDir, shaA := writeRetryTestRepo(t, filepath.Join(t.TempDir(), "repo-a"), "git@example.test:owner/repo-a.git", "behavior-a")
	if err := os.WriteFile(filepath.Join(repoDir, ".sparkwing", "behavior.txt"), []byte("behavior-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForRetryTest(t, repoDir, "add", ".sparkwing/behavior.txt")
	runGitForRetryTest(t, repoDir, "commit", "-m", "change behavior")

	trig := &store.Trigger{
		Pipeline: "pre-push",
		Repo:     "owner/repo-a",
		RetryOf:  "source-run",
		TriggerEnv: map[string]string{
			retryprovenance.RepoDirKey:      repoDir,
			retryprovenance.RepoIdentityKey: "git@example.test:owner/repo-a.git",
			retryprovenance.RevisionKey:     shaA,
			retryprovenance.PlanHashKey:     "sha256:matching-plan-shape",
		},
	}

	_, err := locateTriggerRepo(context.Background(), trig, "")
	var unavailable *RetrySourceUnavailableError
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "revision drift") {
		t.Fatalf("error=%T %v, want typed revision drift", err, err)
	}
}

func TestPrepareTriggerRepo_RetrySnapshotsRecordedRevisionDespiteDirtySource(t *testing.T) {
	repoDir, sha := writeRetryTestRepo(t, filepath.Join(t.TempDir(), "repo-a"), "git@example.test:owner/repo-a.git", "recorded-behavior")
	behaviorPath := filepath.Join(repoDir, ".sparkwing", "behavior.txt")
	executablePath := filepath.Join(repoDir, ".sparkwing", "main.go")
	if err := os.WriteFile(behaviorPath, []byte("dirty-behavior-with-matching-plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executablePath, []byte(retryExecutableSource("dirty-executable-with-matching-plan")), 0o644); err != nil {
		t.Fatal(err)
	}
	trig := &store.Trigger{
		Pipeline: "pre-push",
		Repo:     "owner/repo-a",
		RetryOf:  "source-run",
		TriggerEnv: map[string]string{
			retryprovenance.RepoDirKey:      repoDir,
			retryprovenance.RepoIdentityKey: "git@example.test:owner/repo-a.git",
			retryprovenance.RevisionKey:     sha,
			retryprovenance.PlanHashKey:     "sha256:matching-plan-shape",
		},
	}

	snapshotDir, cleanup, err := prepareTriggerRepo(context.Background(), trig, "")
	if err != nil {
		t.Fatalf("prepareTriggerRepo: %v", err)
	}
	if snapshotDir == repoDir {
		cleanup()
		t.Fatal("retry returned the mutable source checkout instead of a snapshot")
	}
	if err := os.WriteFile(behaviorPath, []byte("later-toctou-behavior"), 0o644); err != nil {
		cleanup()
		t.Fatal(err)
	}
	if err := os.WriteFile(executablePath, []byte(retryExecutableSource("later-toctou-executable")), 0o644); err != nil {
		cleanup()
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(snapshotDir, ".sparkwing", "behavior.txt"))
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	if string(raw) != "recorded-behavior" {
		cleanup()
		t.Fatalf("snapshot behavior=%q, want committed recorded behavior", raw)
	}
	if got := strings.TrimSpace(runGitForRetryTest(t, snapshotDir, "rev-parse", "HEAD")); !strings.EqualFold(got, sha) {
		cleanup()
		t.Fatalf("snapshot revision=%q, want %q", got, sha)
	}
	cleanup()
	if _, err := os.Stat(snapshotDir); !os.IsNotExist(err) {
		t.Fatalf("snapshot survived cleanup: %v", err)
	}
	raw, err = os.ReadFile(behaviorPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "later-toctou-behavior" {
		t.Fatalf("cleanup changed source worktree behavior=%q", raw)
	}

	outputPath := filepath.Join(t.TempDir(), "executed-behavior")
	t.Setenv("SPARKWING_RETRY_TEST_OUTPUT", outputPath)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := dispatchLocalTrigger(context.Background(), nil, trig, "", "", &localCompileCache{}, logger); err != nil {
		t.Fatalf("dispatchLocalTrigger: %v", err)
	}
	raw, err = os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "recorded-behavior" {
		t.Fatalf("retry executed %q, want the recorded executable behavior", raw)
	}
}

func writeRetryTestRepo(t *testing.T, dir, remoteURL, behavior string) (string, string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "pipelines:\n  - name: pre-push\n    steps:\n      - shared-step\n"
	if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "behavior.txt"), []byte(behavior), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "go.mod"), []byte("module retrytest\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "main.go"), []byte(retryExecutableSource(behavior)), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForRetryTest(t, dir, "init")
	runGitForRetryTest(t, dir, "config", "user.email", "retry@example.test")
	runGitForRetryTest(t, dir, "config", "user.name", "Retry Test")
	runGitForRetryTest(t, dir, "remote", "add", "origin", remoteURL)
	runGitForRetryTest(t, dir, "add", ".sparkwing")
	runGitForRetryTest(t, dir, "commit", "-m", "initial")
	sha := strings.TrimSpace(runGitForRetryTest(t, dir, "rev-parse", "HEAD"))
	return dir, sha
}

func retryExecutableSource(behavior string) string {
	return fmt.Sprintf(`package main

import "os"

func main() {
	if err := os.WriteFile(os.Getenv("SPARKWING_RETRY_TEST_OUTPUT"), []byte(%q), 0o644); err != nil {
		panic(err)
	}
}
`, behavior)
}

func runGitForRetryTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
