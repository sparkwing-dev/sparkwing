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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
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

func TestLocalImplicitAwaitRetainsParentProvenanceWithoutForcingRegistryLookup(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateRun(ctx, store.Run{
		ID: "parent", Pipeline: "release", Status: "running",
		TriggerSource: "pipeline-working-tree@laptop.local",
		Repo:          "sparkwing-dev/sparkwing", RepoURL: "git@github.com:sparkwing-dev/sparkwing.git",
		GithubOwner: "sparkwing-dev", GithubRepo: "sparkwing",
	}); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"CALLER_VALUE": "unchanged"}
	id, err := (localState{st: st}).EnqueueTriggerWithEnv(
		ctx, "template-verify", nil, "parent", "gate", "", "await-pipeline", "", "", "", env,
	)
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := st.GetTrigger(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.Repo != "sparkwing-dev/sparkwing" {
		t.Fatalf("Repo = %q, want inherited provenance", trigger.Repo)
	}
	if !trigger.RepoInherited {
		t.Fatal("RepoInherited = false, want true")
	}
	if trigger.TriggerSource != "pipeline-working-tree@laptop.local" {
		t.Fatalf("TriggerSource = %q, want parent workspace placement", trigger.TriggerSource)
	}
	if len(env) != 1 || env["CALLER_VALUE"] != "unchanged" {
		t.Fatalf("caller trigger env mutated: %#v", env)
	}
}

func TestRunLocalTriggerLoopClaimsPendingTriggerImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateRun(ctx, store.Run{
		ID: "parent", Pipeline: "parent", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	childID, err := (localState{st: st}).EnqueueTrigger(
		ctx, "child", nil, "parent", "gate", "", "await-pipeline", "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		runLocalTriggerLoop(ctx, localState{st: st}, "parent", "", t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, childStoreEnv{})
	}()
	t.Cleanup(func() {
		cancel()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			t.Error("local trigger loop did not stop")
		}
	})

	deadline := time.NewTimer(400 * time.Millisecond)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		run, err := st.GetRun(ctx, childID)
		if err == nil {
			if run.Status != "failed" {
				t.Fatalf("child status = %q, want failed dispatch", run.Status)
			}
			return
		}
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("get child run: %v", err)
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("pending child was not claimed within 400ms")
		}
	}
}

func TestExplicitAwaitNeverTrustsReservedLookingTriggerEnvironment(t *testing.T) {
	trigger := &store.Trigger{
		Repo:       "owner/other",
		TriggerEnv: map[string]string{"SPARKWING_AWAIT_REPO_INHERITED": "1"},
	}
	if triggerUsesParentRepo(trigger) {
		t.Fatal("explicit cross-repository await selected the parent checkout")
	}
}

func TestDispatchLocalTrigger_RunAndAwaitCachedExecutableSurvivesCacheRemovalWhileParentLives(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not allow the describe process to unlink its running executable")
	}
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	repoDir := t.TempDir()
	sparkwingDir := filepath.Join(repoDir, ".sparkwing")
	if err := os.MkdirAll(sparkwingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "go.mod"), []byte("module cacheleasefixture\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "main.go"), []byte(cacheLeaseExecutableSource), 0o644); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(repoDir, "child-runs")
	t.Setenv("SPARKWING_CACHE_LEASE_TEST_OUTPUT", output)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cache := &localCompileCache{}
	t.Cleanup(func() { _ = cache.Close() })
	dispatch := func(id string) error {
		return dispatchLocalTrigger(context.Background(), &store.Trigger{
			ID:           id,
			Pipeline:     "child",
			ParentRunID:  "parent-live",
			ParentNodeID: "spawn-child",
		}, "", repoDir, cache, logger, nil)
	}

	if err := dispatch("child-one"); err != nil {
		t.Fatalf("prime cached child dispatch: %v", err)
	}

	result, err := bincache.Prune(context.Background(), bincache.PruneOptions{ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("prune shared pipeline entry: %v", err)
	}
	if result.ReclaimedEntries != 1 {
		t.Fatalf("prune result = %+v, want one reclaimed entry", result)
	}
	if err := dispatch("child-two"); err != nil {
		t.Fatalf("cached child dispatch after concurrent cache removal: %v", err)
	}

	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), "child-one\nchild-two\n"; got != want {
		t.Fatalf("child executions = %q, want %q", got, want)
	}
}

func TestExecLocalChild_MissingLeaseExplainsCacheProvenanceAndRecovery(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-pipeline")
	err := execLocalChild(context.Background(), missing, t.TempDir(), []string{"handle-trigger", "child"}, nil)
	if err == nil {
		t.Fatal("missing executable unexpectedly ran")
	}
	for _, want := range []string{
		"child executable lease",
		"pipeline-cache provenance",
		"Re-run the parent pipeline",
		"sparkwing pipeline sparks warmup --clear-cache",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing-executable error lacks %q: %v", want, err)
		}
	}
}

const cacheLeaseExecutableSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--describe" {
		fmt.Print("[{\"name\":\"child\"}]")
		return
	}
	f, err := os.OpenFile(os.Getenv("SPARKWING_CACHE_LEASE_TEST_OUTPUT"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, os.Args[len(os.Args)-1]); err != nil {
		panic(err)
	}
}
`

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

func TestLocateTriggerRepo_RetryRejectsARevisionThatIsNotAnObjectID(t *testing.T) {
	repoDir, _ := writeRetryTestRepo(t, filepath.Join(t.TempDir(), "repo-a"), "git@example.test:owner/repo-a.git", "recorded-behavior")
	for _, revision := range []string{"--upload-pack=evil", "-x", "HEAD", "main", "abc1234"} {
		trig := &store.Trigger{
			Pipeline: "pre-push",
			Repo:     "owner/repo-a",
			RetryOf:  "source-run",
			TriggerEnv: map[string]string{
				retryprovenance.RepoDirKey:      repoDir,
				retryprovenance.RepoIdentityKey: "git@example.test:owner/repo-a.git",
				retryprovenance.RevisionKey:     revision,
				retryprovenance.PlanHashKey:     "sha256:matching-plan-shape",
			},
		}
		_, err := locateTriggerRepo(context.Background(), trig, "")
		var unavailable *RetrySourceUnavailableError
		if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "not a git object id") {
			t.Errorf("revision %q: error=%T %v, want a rejected object id", revision, err, err)
		}
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
	leaseRoot := t.TempDir()
	t.Setenv("TMPDIR", leaseRoot)
	t.Cleanup(func() {
		matches, err := filepath.Glob(filepath.Join(leaseRoot, "sparkwing-child-executables-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Errorf("child executable leases survived test cleanup: %v", matches)
		}
	})

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
	cache := &localCompileCache{}
	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Errorf("close local compile cache: %v", err)
		}
	})
	if err := dispatchLocalTrigger(context.Background(), trig, "", "", cache, logger, nil); err != nil {
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
