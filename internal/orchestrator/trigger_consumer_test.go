package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func consumerTestStore(t *testing.T, home string) *store.Store {
	t.Helper()
	p := PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// seedSubmission writes the trigger + pending run pair `runs submit`
// persists, pointed at repoDir so the consumer can locate it.
func seedSubmission(t *testing.T, st *store.Store, id, pipeline, repoDir string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	env := map[string]string{}
	if repoDir != "" {
		env[SubmitRepoDirKey] = repoDir
	}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: id, Pipeline: pipeline, CreatedAt: now,
		TriggerSource: "runs-submit", TriggerEnv: env,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: id, Pipeline: pipeline, Status: "pending",
		TriggerSource: "runs-submit", CreatedAt: now, StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// waitFor polls cond until it holds or the deadline passes. Polling
// beats sleeping: these tests assert on a background loop whose timing
// varies with machine load, and a fixed sleep either flakes or is slow.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		<-poll.C
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func expireTriggerLease(t *testing.T, st *store.Store, id string) {
	t.Helper()
	result, err := st.DB().Exec(
		`UPDATE triggers SET lease_expires_at = ? WHERE id = ? AND status = 'claimed' AND lease_expires_at IS NOT NULL`,
		time.Now().Add(-time.Second).UnixNano(), id)
	if err != nil {
		t.Fatalf("expire trigger %q lease: %v", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("count expired trigger %q leases: %v", id, err)
	}
	if rows != 1 {
		t.Fatalf("expired trigger %q leases = %d, want 1", id, rows)
	}
}

// TestServeConsumer_OneConsumerPerHome is the no-double-consume proof at
// the primitive that enforces it. Two consumers on one home would each
// claim triggers, and a home with both a dashboard and a resident
// consumer is the ordinary case, not an exotic one.
func TestServeConsumer_OneConsumerPerHome(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan struct{})
	served := make(chan error, 1)
	go func() {
		served <- ServeConsumer(ctx, ConsumerOptions{
			Home: home, Store: st, Logger: quietLogger(),
			IdleTimeout: -1, Ready: ready,
		})
	}()
	<-ready

	if running, err := ConsumerRunning(home); err != nil || !running {
		t.Fatalf("ConsumerRunning = %v, %v; want true while serving", running, err)
	}
	if pid, ok := ConsumerPID(home); !ok || pid != os.Getpid() {
		t.Fatalf("ConsumerPID = %d, %v; want this process", pid, ok)
	}

	second := ServeConsumer(ctx, ConsumerOptions{
		Home: home, Store: st, Logger: quietLogger(), IdleTimeout: -1,
	})
	if !errors.Is(second, ErrConsumerElectionLost) {
		t.Fatalf("second consumer err = %v, want ErrConsumerElectionLost", second)
	}

	cancel()
	if err := <-served; err != nil {
		t.Fatalf("first consumer returned %v", err)
	}
	waitFor(t, "the lock to be released after the consumer stops", 2*time.Second, func() bool {
		running, err := ConsumerRunning(home)
		return err == nil && !running
	})
}

// TestConsumerRunning_FalseBeforeAnyConsumerHasRun pins that a home
// nobody has ever consumed reads as idle rather than erroring, so
// `runs submit` on a fresh machine spawns instead of failing.
func TestConsumerRunning_FalseBeforeAnyConsumerHasRun(t *testing.T) {
	running, err := ConsumerRunning(t.TempDir())
	if err != nil {
		t.Fatalf("ConsumerRunning on a fresh home: %v", err)
	}
	if running {
		t.Fatal("a home with no lock file reports a running consumer")
	}
}

// TestRunLocalTriggerConsumer_StandsDownWhenAResidentConsumerHoldsTheLock
// is the dashboard half of the exclusion. The dashboard has always
// hosted a consumer; now that a standalone one exists, starting a
// dashboard must not add a second claimer to the same queue.
func TestRunLocalTriggerConsumer_StandsDownWhenAResidentConsumerHoldsTheLock(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan struct{})
	go func() {
		_ = ServeConsumer(ctx, ConsumerOptions{
			Home: home, Store: st, Logger: quietLogger(),
			IdleTimeout: -1, Ready: ready,
		})
	}()
	<-ready
	residentPID, _ := ConsumerPID(home)

	if err := RunLocalTriggerConsumer(ctx, home, st, quietLogger()); err != nil {
		t.Fatalf("dashboard consumer startup: %v", err)
	}
	// The dashboard's consumer loses the election in its own goroutine.
	// The observable consequence is that the resident consumer still owns
	// the lock and the pid file was never rewritten.
	time.Sleep(200 * time.Millisecond)
	pid, ok := ConsumerPID(home)
	if !ok || pid != residentPID {
		t.Fatalf("consumer pid = %d (ok=%v), want the resident %d still owning the queue", pid, ok, residentPID)
	}
}

// TestServeConsumer_IdleExitReleasesTheHome covers the other side of
// residency: a laptop that submitted one run in the morning must not
// carry a consumer process all day.
func TestServeConsumer_IdleExitReleasesTheHome(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)

	done := make(chan error, 1)
	go func() {
		done <- ServeConsumer(context.Background(), ConsumerOptions{
			Home: home, Store: st, Logger: quietLogger(),
			IdleTimeout: 150 * time.Millisecond,
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("idle consumer returned %v, want a clean exit", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("consumer never exited on an elapsed idle window")
	}
	if running, err := ConsumerRunning(home); err != nil || running {
		t.Fatalf("ConsumerRunning after idle exit = %v, %v; want false", running, err)
	}
}

// TestShouldExit_KeepsServingWhileWorkIsQueued is the guarantee that
// makes an acknowledgment mean something. If the consumer could leave
// with a pending trigger in the table, a run submitted near an idle
// boundary would silently never start.
func TestShouldExit_KeepsServingWhileWorkIsQueued(t *testing.T) {
	st := consumerTestStore(t, t.TempDir())
	seedSubmission(t, st, "run-queued", "deploy", "")

	released := false
	rt := consumerRuntime{
		idle:   time.Millisecond,
		unlock: func() { released = true },
		relock: func() bool { return true },
	}
	if rt.shouldExit(context.Background(), st, quietLogger(), time.Now().Add(-time.Minute)) {
		t.Fatal("consumer decided to exit with a pending trigger on the queue")
	}
	if released {
		t.Fatal("consumer handed back the queue lock while work was pending")
	}
}

// TestShouldExit_StaysWhenTheIdleWindowHasNotElapsed keeps the decision
// from firing on the first quiet poll.
func TestShouldExit_StaysWhenTheIdleWindowHasNotElapsed(t *testing.T) {
	st := consumerTestStore(t, t.TempDir())
	rt := consumerRuntime{idle: time.Hour, unlock: func() {}, relock: func() bool { return true }}
	if rt.shouldExit(context.Background(), st, quietLogger(), time.Now()) {
		t.Fatal("consumer exited before its idle window elapsed")
	}
}

// TestShouldExit_NeverExitsWhenIdleIsDisabled covers the
// dashboard-hosted loop, which lives as long as the dashboard does.
func TestShouldExit_NeverExitsWhenIdleIsDisabled(t *testing.T) {
	st := consumerTestStore(t, t.TempDir())
	rt := consumerRuntime{idle: -1}
	if rt.shouldExit(context.Background(), st, quietLogger(), time.Now().Add(-24*time.Hour)) {
		t.Fatal("a consumer with idle exit disabled decided to exit")
	}
}

// TestShouldExit_ResumesWhenWorkArrivesDuringTheHandover is the
// interleaving the design is built around, and the one a reviewer should
// be most suspicious of.
//
// A submitter persists its trigger and then asks whether a consumer is
// running; this consumer counts pending work and then releases its lock.
// If the insert lands between the count and the release, the submitter
// sees a held lock and does not spawn, while the consumer saw an empty
// queue. Counting once more after the lock is free is what stops that
// from stranding an acknowledged run. The fake unlock here inserts the
// trigger at exactly that instant.
func TestShouldExit_ResumesWhenWorkArrivesDuringTheHandover(t *testing.T) {
	st := consumerTestStore(t, t.TempDir())
	relocked := false
	rt := consumerRuntime{
		idle: time.Millisecond,
		unlock: func() {
			seedSubmission(t, st, "run-raced-in", "deploy", "")
		},
		relock: func() bool { relocked = true; return true },
	}
	if rt.shouldExit(context.Background(), st, quietLogger(), time.Now().Add(-time.Minute)) {
		t.Fatal("consumer exited and stranded a run submitted during the lock handover")
	}
	if !relocked {
		t.Fatal("consumer resumed without taking the queue lock back")
	}
}

// TestShouldExit_StandsDownWhenAnotherConsumerTookTheQueue covers the
// same handover when a fresh consumer wins the freed lock first: this
// one must leave rather than fight for it, since the work is owned.
func TestShouldExit_StandsDownWhenAnotherConsumerTookTheQueue(t *testing.T) {
	st := consumerTestStore(t, t.TempDir())
	rt := consumerRuntime{
		idle:   time.Millisecond,
		unlock: func() { seedSubmission(t, st, "run-raced-in", "deploy", "") },
		relock: func() bool { return false },
	}
	if !rt.shouldExit(context.Background(), st, quietLogger(), time.Now().Add(-time.Minute)) {
		t.Fatal("consumer kept serving a queue another consumer owns")
	}
}

// TestConsumer_CancelRequestedBeforeClaimNeverDispatches closes the race
// between an operator's cancel and a consumer's claim. The claim can win
// the transaction, and without this check the run would start anyway --
// a cancellation the system accepted and then ignored.
func TestConsumer_CancelRequestedBeforeClaimNeverDispatches(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-cancelme", "never-runs", "")

	// Claim it the way the loop does, then request cancellation, which is
	// exactly the state the loop's pre-dispatch check exists for.
	claimed, err := st.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RequestCancel(ctx, claimed.ID); err != nil {
		t.Fatal(err)
	}

	if !cancelClaimedTriggerIfRequested(ctx, st, claimed, time.Minute, quietLogger()) {
		t.Fatal("a cancel-requested claim was not recognized before dispatch")
	}
	run, err := st.GetRun(ctx, "run-cancelme")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled", run.Status)
	}
	trig, err := st.GetTrigger(ctx, "run-cancelme")
	if err != nil {
		t.Fatal(err)
	}
	if trig.Status != "done" {
		t.Fatalf("trigger status = %q, want done", trig.Status)
	}
}

// TestConsumer_NoCancelRequestedDispatchesNormally is the negative half:
// the pre-dispatch check must not swallow ordinary work.
func TestConsumer_NoCancelRequestedDispatchesNormally(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-normal", "runs-fine", "")
	claimed, err := st.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cancelClaimedTriggerIfRequested(ctx, st, claimed, time.Minute, quietLogger()) {
		t.Fatal("an uncancelled claim was treated as cancelled")
	}
}

// TestSubmittedTriggerRepoDir_SelectsTheSubmittingCheckout is the repo
// threading the whole submission path depends on. Two checkouts of one
// project declare the same pipeline names, and the registry cannot tell
// them apart; the recorded directory can.
func TestSubmittedTriggerRepoDir_SelectsTheSubmittingCheckout(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	trig := &store.Trigger{
		ID: "run-1", Pipeline: "lint",
		TriggerEnv: map[string]string{SubmitRepoDirKey: repoDir},
	}
	got, err := locateTriggerRepo(context.Background(), trig, t.TempDir())
	if err != nil {
		t.Fatalf("locateTriggerRepo: %v", err)
	}
	if got != repoDir {
		t.Fatalf("located %q, want the submitting checkout %q", got, repoDir)
	}
}

// TestSubmittedTriggerRepoDir_FailsClosedWhenTheCheckoutIsGone pins that
// a vanished checkout is an error rather than a quiet fallback. Falling
// back to the registry would execute a different copy of the pipeline
// than the person submitted, which is the confusion recording the path
// exists to prevent.
func TestSubmittedTriggerRepoDir_FailsClosedWhenTheCheckoutIsGone(t *testing.T) {
	trig := &store.Trigger{
		ID: "run-1", Pipeline: "lint",
		TriggerEnv: map[string]string{SubmitRepoDirKey: filepath.Join(t.TempDir(), "gone")},
	}
	_, err := locateTriggerRepo(context.Background(), trig, "")
	if err == nil {
		t.Fatal("a submitted trigger whose checkout vanished resolved anyway")
	}
	if !strings.Contains(err.Error(), "no longer has a .sparkwing/") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

// TestSubmittedTriggerRepoDir_AbsentOnOrdinaryTriggers keeps the new
// branch out of the way of every trigger the webhook, spawn, and retry
// paths create.
func TestSubmittedTriggerRepoDir_AbsentOnOrdinaryTriggers(t *testing.T) {
	dir, err := submittedTriggerRepoDir(&store.Trigger{ID: "run-1", Pipeline: "lint"})
	if err != nil || dir != "" {
		t.Fatalf("submittedTriggerRepoDir = %q, %v; want empty and no error", dir, err)
	}
}

// TestEnsureRunLogDir_OnlyNamesADirectoryThatExists holds the log_path
// rule the submission acknowledgment inherits: the field is either a
// real directory or absent, never a plausible-looking path to nothing.
func TestEnsureRunLogDir_OnlyNamesADirectoryThatExists(t *testing.T) {
	p := PathsAt(t.TempDir())
	dir := EnsureRunLogDir(p, "run-1")
	if dir == "" {
		t.Fatal("EnsureRunLogDir returned empty for a writable home")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("EnsureRunLogDir named %q, which is not a directory: %v", dir, err)
	}

	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := EnsureRunLogDir(PathsAt(blocked), "run-1"); got != "" {
		t.Fatalf("EnsureRunLogDir = %q for an uncreatable home, want empty", got)
	}
}

// TestSweeper_LeavesALiveRunningDispatchAlone covers suspension advancing wall
// time past a live dispatch's lease while its monotonic heartbeat ticker stops.
// The sweeper can therefore observe a lapsed lease before the next heartbeat.
//
// A lapsed lease alone is not evidence of death: only a run that never
// started is requeued.
func TestSweeper_LeavesALiveRunningDispatchAlone(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-alive", "deploy", "")

	if _, err := st.ClaimNextTrigger(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	// The dispatch started: a child process is executing.
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-alive", Pipeline: "deploy", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// The laptop wakes: wall time has jumped past the lease while the
	// heartbeat's next monotonic tick is still far away.
	if _, err := st.DB().Exec(
		`UPDATE triggers SET lease_expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Hour).UnixNano(), "run-alive"); err != nil {
		t.Fatal(err)
	}

	requeueExpiredClaims(ctx, st, newInFlightSet(), quietLogger())

	trig, err := st.GetTrigger(ctx, "run-alive")
	if err != nil {
		t.Fatal(err)
	}
	if trig.Status == "pending" {
		t.Fatal("a live running dispatch was swept back onto the queue; it would execute twice")
	}
	if _, err := st.ClaimNextTrigger(ctx, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the live run became re-claimable (err=%v)", err)
	}
}

// TestSweeper_NeverRequeuesWhatThisConsumerIsExecuting covers the window where
// a consumer owns a live local dispatch that the store cannot distinguish
// from a dead one.
func TestSweeper_NeverRequeuesWhatThisConsumerIsExecuting(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-mine", "deploy", "")
	if _, err := st.ClaimNextTrigger(ctx, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	expireTriggerLease(t, st, "run-mine")

	// The run row is still pending -- the child has been exec'd but has
	// not reached CreateRun yet, which is the widest version of the
	// window. Only the in-flight set knows this work is owned.
	inFlight := newInFlightSet()
	inFlight.add("run-mine")

	requeueExpiredClaims(ctx, st, inFlight, quietLogger())

	trig, err := st.GetTrigger(ctx, "run-mine")
	if err != nil {
		t.Fatal(err)
	}
	if trig.Status == "pending" {
		t.Fatal("the consumer swept its own in-flight dispatch back onto its own queue")
	}
}

// TestSweeper_ClosesOutAClaimWhoseRunAlreadyEnded pins the reconcile
// step, and that it happens before anything is requeued.
func TestSweeper_ClosesOutAClaimWhoseRunAlreadyEnded(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-done", "deploy", "")
	if _, err := st.ClaimNextTrigger(ctx, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, "run-done", "success", ""); err != nil {
		t.Fatal(err)
	}
	expireTriggerLease(t, st, "run-done")

	requeueExpiredClaims(ctx, st, newInFlightSet(), quietLogger())

	trig, err := st.GetTrigger(ctx, "run-done")
	if err != nil {
		t.Fatal(err)
	}
	if trig.Status != "done" {
		t.Fatalf("trigger status = %q, want done", trig.Status)
	}
	if _, err := st.ClaimNextTrigger(ctx, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a finished run was put back on the queue (err=%v)", err)
	}
}

// TestSweeper_StillRecoversAConsumerKilledBeforeTheRunStarted ensures that
// protecting live dispatches does not prevent recovery before a run starts.
func TestSweeper_StillRecoversAConsumerKilledBeforeTheRunStarted(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-orphan", "deploy", "")
	if _, err := st.ClaimNextTrigger(ctx, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	expireTriggerLease(t, st, "run-orphan")

	requeueExpiredClaims(ctx, st, newInFlightSet(), quietLogger())

	got, err := st.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatalf("a run whose consumer died before it started was not recovered: %v", err)
	}
	if got.ID != "run-orphan" {
		t.Fatalf("claimed %q", got.ID)
	}
}

// TestDashboardConsumer_RetakesTheQueueAfterTheResidentIdlesOut covers the
// ordering "resident consumer up, dashboard starts, resident idles out". The
// dashboard used to attempt the election once and stand down forever, leaving
// the home with no consumer.
func TestDashboardConsumer_RetakesTheQueueAfterTheResidentIdlesOut(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan struct{})
	residentDone := make(chan error, 1)
	go func() {
		residentDone <- ServeConsumer(ctx, ConsumerOptions{
			Home: home, Store: st, Logger: quietLogger(),
			IdleTimeout: 300 * time.Millisecond, Ready: ready,
		})
	}()
	<-ready

	// The dashboard comes up second and loses the election.
	if err := RunLocalTriggerConsumer(ctx, home, st, quietLogger()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	select {
	case <-residentDone:
	case <-time.After(15 * time.Second):
		t.Fatal("resident consumer never idled out")
	}

	// Work arrives with only the dashboard left. It must pick the queue
	// back up rather than leave the run pending forever.
	seedSubmission(t, st, "run-stranded", "deploy", "")

	waitFor(t, "the dashboard consumer to retake the queue", 20*time.Second, func() bool {
		running, err := ConsumerRunning(home)
		return err == nil && running
	})
	waitFor(t, "the stranded trigger to be claimed", 20*time.Second, func() bool {
		trig, err := st.GetTrigger(context.Background(), "run-stranded")
		return err == nil && trig.Status != "pending"
	})
}

type transientHeartbeatStore struct {
	calls int
	seq   int64
}

func (s *transientHeartbeatStore) HeartbeatTrigger(context.Context, string, time.Duration) (bool, error) {
	s.calls++
	if s.calls == 1 {
		return false, errors.New("database is locked")
	}
	return false, nil
}

func (s *transientHeartbeatStore) TriggerClaimGeneration(context.Context, string) (int64, error) {
	return s.seq, nil
}

// TestHeartbeat_SurvivesATransientStoreError covers a heartbeat that used to
// give up permanently on the first error that was not ErrNotFound, so
// one "database is locked" from a concurrent writer left the claim
// undefended for the rest of a long run -- which is what let the sweeper
// requeue a live dispatch.
func TestHeartbeat_SurvivesATransientStoreError(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-hb", "deploy", "")
	claimed, err := st.ClaimNextTrigger(ctx, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	transient := &transientHeartbeatStore{seq: claimed.ClaimSeq}
	if !heartbeatOnce(ctx, transient, "run-hb", claimed.ClaimSeq, 30*time.Second, time.Second, quietLogger()) {
		t.Fatal("heartbeat gave up on a healthy claim")
	}
	if transient.calls != 2 {
		t.Fatalf("heartbeat attempts = %d, want 2 after one transient failure", transient.calls)
	}

	// A claim that no longer exists is the one case that ends it.
	if err := st.FinishTrigger(ctx, "run-hb"); err != nil {
		t.Fatal(err)
	}
	if heartbeatOnce(ctx, st, "run-hb", claimed.ClaimSeq, 30*time.Second, 50*time.Millisecond, quietLogger()) {
		t.Fatal("heartbeat kept defending a claim that no longer exists")
	}
}

// TestHeartbeat_StopsWhenTheClaimIsSuperseded keeps a stale dispatch
// from prolonging a lease the current claim owns.
func TestHeartbeat_StopsWhenTheClaimIsSuperseded(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-super", "deploy", "")
	stale, err := st.ClaimNextTrigger(ctx, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	expireTriggerLease(t, st, "run-super")
	if _, err := st.RequeueUnstartedClaim(ctx, "run-super"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimNextTrigger(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}

	if heartbeatOnce(ctx, st, "run-super", stale.ClaimSeq, time.Minute, 50*time.Millisecond, quietLogger()) {
		t.Fatal("a superseded dispatch kept renewing a claim it no longer holds")
	}
}
