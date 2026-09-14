//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunDetached_UsesEachSubmissionEnvironment(t *testing.T) {
	e := newSubmitTestEnv(t)
	e.extraEnv = []string{"SPARKWING_SUBMIT_TEST_ENV=first"}
	e.submit()
	waitUntil(t, "first submitted environment", 90*time.Second, func() bool {
		return len(linesIn(e.envMarker)) == 1
	})

	e.extraEnv = []string{"SPARKWING_SUBMIT_TEST_ENV=second"}
	e.submit()
	waitUntil(t, "second submitted environment", 90*time.Second, func() bool {
		return len(linesIn(e.envMarker)) == 2
	})

	if got := linesIn(e.envMarker); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("execution environments = %q, want one snapshot from each submission", got)
	}
}

func TestRunDetached_ExecutionOutlivesTheSubmittingProcess(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	ack := e.submit()
	if ack.RunID == "" {
		t.Fatal("submit acknowledged without a run id")
	}
	if ack.LogPath == "" {
		t.Fatal("submit acknowledged without a log path")
	}
	if info, err := os.Stat(ack.LogPath); err != nil || !info.IsDir() {
		t.Fatalf("acknowledged log_path %q is not a directory: %v", ack.LogPath, err)
	}

	var pid int
	waitUntil(t, "a detached consumer to hold the queue", 10*time.Second, func() bool {
		var ok bool
		pid, ok = orchestrator.ConsumerPID(e.home)
		return ok
	})
	if pid == os.Getpid() {
		t.Fatal("the test process is hosting the consumer; the run is not detached")
	}

	st := e.store()
	if _, err := st.GetTrigger(context.Background(), ack.RunID); err != nil {
		t.Fatalf("acknowledged run has no trigger row: %v", err)
	}
	if _, err := st.GetRun(context.Background(), ack.RunID); err != nil {
		t.Fatalf("acknowledged run has no run row: %v", err)
	}

	waitUntil(t, "the detached consumer to execute the submitted run", 90*time.Second, func() bool {
		lines := e.markerLines()
		return len(lines) == 1 && lines[0] == ack.RunID
	})
}

func TestRunsRetry_HeadlessLocalQueueExecutesFailedAndFullScopes(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	e.extraEnv = []string{"SPARKWING_CONTROLLER_URL=http://127.0.0.1:1", "SPARKWING_LOGS_URL="}
	runSnapshotGit(t, e.repoDir, "init")
	runSnapshotGit(t, e.repoDir, "config", "user.email", "retry@example.test")
	runSnapshotGit(t, e.repoDir, "config", "user.name", "Retry Test")
	runSnapshotGit(t, e.repoDir, "remote", "add", "origin", "https://example.test/acme/retry-fixture.git")
	runSnapshotGit(t, e.repoDir, "add", ".sparkwing")
	runSnapshotGit(t, e.repoDir, "commit", "-m", "fixture")
	revision := strings.TrimSpace(runSnapshotGit(t, e.repoDir, "rev-parse", "HEAD"))

	const sourceID = "run-headless-retry-source"
	st := e.store()
	if err := st.CreateRun(context.Background(), store.Run{
		ID:           sourceID,
		Pipeline:     "fixture",
		Status:       "failed",
		GitBranch:    "main",
		GitSHA:       revision,
		Repo:         "acme/retry-fixture",
		RepoURL:      "https://example.test/acme/retry-fixture.git",
		PlanSnapshot: []byte(`{"pipeline":"fixture","nodes":[]}`),
		Invocation:   map[string]any{"cwd": e.repoDir},
		StartedAt:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		flag string
		full bool
	}{
		{flag: "--failed"},
		{flag: "--all", full: true},
	} {
		out := e.mustRun("runs", "retry", tc.flag, "--run", sourceID)
		fields := strings.Fields(out)
		if len(fields) < 2 || fields[0] != "run" {
			t.Fatalf("retry output did not start with the new run id:\n%s", out)
		}
		retryID := fields[1]
		trigger, err := st.GetTrigger(context.Background(), retryID)
		if err != nil {
			t.Fatalf("retry %s has no trigger: %v", retryID, err)
		}
		if trigger.RetryOf != sourceID || trigger.Full != tc.full {
			t.Fatalf("retry trigger = retry_of %q full %v, want %q/%v",
				trigger.RetryOf, trigger.Full, sourceID, tc.full)
		}
		waitUntil(t, tc.flag+" retry to execute without a dashboard", 90*time.Second, func() bool {
			return slices.Contains(e.markerLines(), retryID)
		})
	}
}

func TestRunDetached_DuplicateKeyReturnsTheOriginalRun(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	first := e.submit("--sw-idempotency-key", "deploy-once")
	if first.AlreadySubmitted {
		t.Fatal("the first submission reported itself as a duplicate")
	}
	second := e.submit("--sw-idempotency-key", "deploy-once")
	if second.RunID != first.RunID {
		t.Fatalf("resubmission produced %q, want the original %q", second.RunID, first.RunID)
	}
	if !second.AlreadySubmitted {
		t.Fatal("resubmission did not report already_submitted")
	}

	triggers, err := e.store().ListTriggers(context.Background(), store.TriggerFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(triggers) != 1 {
		t.Fatalf("two submissions under one key created %d triggers, want 1", len(triggers))
	}
}

func TestRunDetached_DistinctKeysAreDistinctRuns(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	a := e.submit("--sw-idempotency-key", "a")
	b := e.submit("--sw-idempotency-key", "b")
	if a.RunID == b.RunID {
		t.Fatalf("distinct keys collapsed onto one run %q", a.RunID)
	}
	if b.AlreadySubmitted {
		t.Fatal("a fresh key was treated as a duplicate")
	}
}

func TestRunDetached_RequestIDDoesNotDeduplicate(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	a := e.submit("--sw-request-id", "trace-1")
	b := e.submit("--sw-request-id", "trace-1")
	if a.RunID == b.RunID {
		t.Fatal("a repeated request id deduplicated the submission")
	}
	if b.AlreadySubmitted {
		t.Fatal("a repeated request id was reported as already submitted")
	}
	trig, err := e.store().GetTrigger(context.Background(), a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := trig.TriggerEnv[SubmitRequestIDKey]; got != "trace-1" {
		t.Fatalf("request id recorded as %q, want trace-1", got)
	}
	if trig.IdempotencyKey != "" {
		t.Fatalf("request id leaked into the idempotency key: %q", trig.IdempotencyKey)
	}
}

func TestRunDetached_PendingWorkRecoversAfterConsumerRestart(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	first := e.submit()
	waitUntil(t, "the first submitted run to execute", 90*time.Second, func() bool {
		return len(e.markerLines()) == 1
	})
	_ = first

	pid, ok := orchestrator.ConsumerPID(e.home)
	if !ok {
		t.Fatal("no consumer to kill")
	}
	if err := signalKill(pid); err != nil {
		t.Fatalf("kill consumer: %v", err)
	}
	waitUntil(t, "the killed consumer's lock to be released", 10*time.Second, func() bool {
		running, err := orchestrator.ConsumerRunning(e.home)
		return err == nil && !running
	})

	st := e.store()
	ctx := context.Background()
	now := time.Now()
	const recovered = "run-recovered"
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: recovered, Pipeline: "fixture", CreatedAt: now, TriggerSource: "runs-submit",
		TriggerEnv: map[string]string{orchestrator.SubmitRepoDirKey: e.repoDir},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: recovered, Pipeline: "fixture", Status: "pending",
		TriggerSource: "runs-submit", CreatedAt: now, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	e.mustRun("runs", "consumer", "start", "--home", e.home)

	waitUntil(t, "the restarted consumer to execute the queued run", 60*time.Second, func() bool {
		lines := e.markerLines()
		return len(lines) == 2 && lines[1] == recovered
	})
}

func TestRunDetached_SeparatorHandsAConflictingFlagToThePipeline(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	out := e.mustRun(append(e.detachArgs("fixture", "--sw-output", "json"),
		"--", "--request-id", "belongs-to-the-pipeline")...)
	var r submitResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("decode ack: %v\n%s", err, out)
	}
	if r.RequestID != "" {
		t.Fatalf("a pipeline argument after `--` was read as the launch's own request id: %q", r.RequestID)
	}
	if _, ok := trigArgsHas(t, e, r.RunID, ""); ok {
		t.Fatal("the bare `--` separator was recorded as an empty-named pipeline argument")
	}
	trig, err := e.store().GetTrigger(context.Background(), r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := trig.Args["request-id"]; got != "belongs-to-the-pipeline" {
		t.Fatalf("pipeline args = %#v, want the flag passed through", trig.Args)
	}
}

func TestRunDetached_RefusesAPipelineNothingDeclares(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	out, err := e.run(e.detachArgs("no-such-pipeline")...)
	if err == nil {
		t.Fatalf("submitting an unknown pipeline succeeded:\n%s", out)
	}
	if !strings.Contains(out, "no-such-pipeline") {
		t.Fatalf("refusal does not name the pipeline:\n%s", out)
	}
	st := e.store()
	triggers, terr := st.ListTriggers(context.Background(), store.TriggerFilter{Limit: 10})
	if terr != nil && !errors.Is(terr, store.ErrNotFound) {
		t.Fatal(terr)
	}
	if len(triggers) != 0 {
		t.Fatalf("a refused submission still queued %d triggers", len(triggers))
	}
}

func TestRunDetached_LiveDispatchSurvivesAWallClockJump(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	holdStarted := e.useBlockingFixture(t)

	ack := e.submit("--sw-consumer-claim-lease", "24s")
	waitUntil(t, "the dispatch to start executing", 120*time.Second, func() bool {
		return e.startsInMarker() >= 1
	})
	waitForFixtureHold(t, holdStarted)

	st := e.store()
	liveBefore, err := st.GetTrigger(context.Background(), ack.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if liveBefore.Status != "claimed" {
		t.Fatalf("live trigger status before jump = %q, want claimed", liveBefore.Status)
	}
	if liveBefore.LeaseExpiresAt == nil {
		t.Fatal("live trigger has no claim lease")
	}
	initialLease := *liveBefore.LeaseExpiresAt

	probeID := ack.RunID + "-sweep-probe"
	now := time.Now()
	if err := st.CreateRun(context.Background(), store.Run{
		ID: probeID, Pipeline: "fixture", Status: "pending", StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(context.Background(), probeID, "success", ""); err != nil {
		t.Fatal(err)
	}

	waitUntil(t, "the live claim heartbeat", 10*time.Second, func() bool {
		live, err := st.GetTrigger(context.Background(), ack.RunID)
		if err != nil {
			t.Fatal(err)
		}
		return live.LeaseExpiresAt != nil && live.LeaseExpiresAt.After(initialLease)
	})
	liveBefore, err = st.GetTrigger(context.Background(), ack.RunID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE triggers SET lease_expires_at = ?
WHERE id = ? AND status = 'claimed' AND claim_seq = ?`,
		time.Now().Add(-time.Hour).UnixNano(), ack.RunID, liveBefore.ClaimSeq)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("expire live claim rows=%d err=%v, want 1", changed, err)
	}
	if _, err := tx.Exec(`
INSERT INTO triggers (id, pipeline, status, created_at, claimed_at, lease_expires_at, claim_seq)
VALUES (?, ?, 'claimed', ?, ?, ?, 1)`, probeID, "fixture", now.UnixNano(), now.UnixNano(),
		now.Add(-time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if e.startsInMarker() > 1 {
			t.Fatalf("run %s was dispatched %d times concurrently after a wall-clock jump:\n  %s",
				ack.RunID, e.startsInMarker(), strings.Join(e.markerLines(), "\n  "))
		}
		probe, err := st.GetTrigger(context.Background(), probeID)
		if err != nil {
			t.Fatal(err)
		}
		if probe.Status == "done" {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("maintenance sweep did not reconcile the probe within 5s; status = %q", probe.Status)
		}
	}
	liveAfter, err := st.GetTrigger(context.Background(), ack.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if liveAfter.LeaseExpiresAt == nil || !liveAfter.LeaseExpiresAt.Before(time.Now()) {
		t.Fatalf("live trigger heartbeat renewed before maintenance observed the expired row: lease = %v", liveAfter.LeaseExpiresAt)
	}
	if liveAfter.Status != liveBefore.Status || liveAfter.ClaimSeq != liveBefore.ClaimSeq {
		t.Fatalf("live trigger changed during sweep: status/claim_seq = %s/%d, want %s/%d",
			liveAfter.Status, liveAfter.ClaimSeq, liveBefore.Status, liveBefore.ClaimSeq)
	}
	if got := e.startsInMarker(); got != 1 {
		t.Fatalf("expected exactly one dispatch, saw %d: %v", got, e.markerLines())
	}
}

func TestRunDetached_IdempotencyKeyDoesNotCrossPipelines(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	other := t.TempDir()
	t.Cleanup(e.stopConsumer)
	otherSparkwing := filepath.Join(other, ".sparkwing")
	if err := os.MkdirAll(otherSparkwing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherSparkwing, "go.mod"),
		[]byte("module otherfixture\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherSparkwing, "main.go"),
		[]byte(strings.Replace(submitFixtureSource, `\"name\":\"fixture\"`, `\"name\":\"beta\"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	first := e.submit("--sw-idempotency-key", "shared-key")
	if first.Pipeline != "fixture" {
		t.Fatalf("first submission is pipeline %q", first.Pipeline)
	}

	out, errOut, rerr := e.runStdout("run", "beta", "--sw-detached", "--sw-cd", other,
		"--sw-output", "json", "--sw-idempotency-key", "shared-key")
	if rerr != nil {
		t.Fatalf("submitting beta failed: %v\nstdout:\n%s\nstderr:\n%s", rerr, out, errOut)
	}
	var second submitResult
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if second.RunID == first.RunID {
		t.Fatalf("submitting beta returned fixture's run %s; beta would never run", first.RunID)
	}
	if second.Pipeline != "beta" {
		t.Fatalf("second submission reported pipeline %q, want beta", second.Pipeline)
	}
	if second.AlreadySubmitted {
		t.Fatal("a key used by a different pipeline was treated as a duplicate")
	}
}

func TestRunDetached_DuplicateKeyWithDifferentArgsIsRefused(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	e.submitWithArgs([]string{"--sw-idempotency-key", "k"}, []string{"--env", "staging"})

	out, err := e.run(append(e.detachArgs("fixture"),
		"--sw-idempotency-key", "k", "--env", "production")...)
	if err == nil {
		t.Fatalf("a key reused with different arguments was accepted:\n%s", out)
	}
	for _, want := range []string{"different arguments", "staging", "production", "new key"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal missing %q:\n%s", want, out)
		}
	}
}

func TestRunDetached_DuplicateAckCarriesTheOriginalStatus(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	first := e.submit("--sw-idempotency-key", "k")

	st := e.store()
	if err := st.FinishRun(context.Background(), first.RunID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}

	out, errOut, rerr := e.runStdout(append(e.detachArgs("fixture", "--sw-output", "json"),
		"--sw-idempotency-key", "k")...)
	if rerr != nil {
		t.Fatalf("resubmit failed: %v\nstdout:\n%s\nstderr:\n%s", rerr, out, errOut)
	}
	var second submitResult
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if second.Status != "failed" {
		t.Fatalf("duplicate ack status = %q, want failed", second.Status)
	}

	pretty := e.mustRun(append(e.detachArgs("fixture", "--sw-output", "pretty"),
		"--sw-idempotency-key", "k")...)
	if !strings.Contains(pretty, "failed") {
		t.Errorf("pretty duplicate ack hides the original's failure:\n%s", pretty)
	}
	if !strings.Contains(pretty, "already finished") {
		t.Errorf("pretty duplicate ack does not say the run is over:\n%s", pretty)
	}
}

func TestRunDetached_ReplacesAConsumerFromAnotherBuild(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)

	old := exec.Command(e.bin, "__runs-consume", "--home", e.home,
		"--idle", "10m", "--version", "v0.0.1-old")
	old.Env = e.env()
	old.Dir = e.repoDir
	logF, _ := os.Create(filepath.Join(e.home, "old-consumer.log"))
	old.Stdout, old.Stderr = logF, logF
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Process.Kill(); _ = old.Wait() })
	waitUntil(t, "the old consumer to take the queue", 20*time.Second, func() bool {
		running, err := orchestrator.ConsumerRunning(e.home)
		return err == nil && running
	})
	oldPID, _ := orchestrator.ConsumerPID(e.home)

	e.submit()

	info, ok := orchestrator.ConsumerInfo(e.home)
	if !ok {
		t.Fatal("no consumer after submitting against an outdated one")
	}
	if info.PID == oldPID {
		t.Fatalf("the outdated consumer (pid %d, v0.0.1-old) still owns the queue; "+
			"an upgrade would never take effect", oldPID)
	}
	if info.Version == "v0.0.1-old" {
		t.Fatalf("replacement consumer still reports the old version %q", info.Version)
	}
	// safety: this waits on the longest chain in this file, a consumer replaced and
	// then a run executed, so it carries the longest bound any wait here uses.
	waitUntil(t, "the new consumer to execute the run", 120*time.Second, func() bool {
		return len(e.markerLines()) >= 1
	})
}

func TestRunsConsumerStop_RecordsTheInterruptedRun(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	holdStarted := e.useBlockingFixture(t)

	ack := e.submit()
	waitUntil(t, "the dispatch to start", 120*time.Second, func() bool {
		return e.startsInMarker() >= 1
	})
	waitForFixtureHold(t, holdStarted)

	e.mustRun("runs", "consumer", "stop", "--home", e.home)

	st := e.store()
	waitUntil(t, "the interrupted run's trigger to leave the claimed state", 30*time.Second, func() bool {
		trig, err := st.GetTrigger(context.Background(), ack.RunID)
		return err == nil && trig.Status != "claimed"
	})
	trig, err := st.GetTrigger(context.Background(), ack.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if trig.Status == "claimed" {
		t.Fatal("stopping the consumer left the run's trigger claimed with nothing executing it")
	}

	if trig.Status == "pending" {
		n, cerr := st.CountPendingTriggers(context.Background())
		if cerr != nil || n == 0 {
			t.Fatalf("trigger reads pending but the queue is empty (n=%d err=%v)", n, cerr)
		}
	}
}

func TestRunDetached_PublishesTheRunHandleFile(t *testing.T) {
	t.Parallel()
	e := newSubmitTestEnv(t)
	handle := filepath.Join(t.TempDir(), "handle.json")

	ack := e.submit("--sw-run-handle-file", handle)

	body, err := os.ReadFile(handle)
	if err != nil {
		t.Fatalf("read published handle: %v", err)
	}
	var published orchestrator.RunHandle
	if err := json.Unmarshal(body, &published); err != nil {
		t.Fatalf("decode published handle: %v\n%s", err, body)
	}
	if published.RunID != ack.RunID || published.Pipeline != ack.Pipeline {
		t.Fatalf("published handle = %+v, want the acknowledged run %s (%s)",
			published, ack.RunID, ack.Pipeline)
	}
	if published.SchemaVersion != orchestrator.RunHandleSchemaVersion {
		t.Errorf("published handle schema_version = %d, want %d",
			published.SchemaVersion, orchestrator.RunHandleSchemaVersion)
	}

	out, rerr := e.run(append(e.detachArgs("fixture"), "--sw-run-handle-file", handle)...)
	if rerr == nil {
		t.Fatalf("a second launch overwrote an existing handle file:\n%s", out)
	}
}
