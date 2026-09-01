package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestConsumerMaintenanceIntervalTracksClaimLease(t *testing.T) {
	tests := []struct {
		name  string
		lease time.Duration
		want  time.Duration
	}{
		{name: "default lease", lease: store.DefaultLeaseDuration, want: 15 * time.Second},
		{name: "short lease", lease: 12 * time.Second, want: time.Second},
		{name: "long lease", lease: 5 * time.Minute, want: 15 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := consumerMaintenanceIntervalForLease(test.lease); got != test.want {
				t.Fatalf("maintenance interval for %s lease = %s, want %s", test.lease, got, test.want)
			}
		})
	}
}

type consumerLogSignal struct {
	message string
	seen    chan struct{}
	once    sync.Once
}

type consumerLogSignalHandler struct {
	base   slog.Handler
	signal *consumerLogSignal
}

func newConsumerLogSignal(message string) (*slog.Logger, *consumerLogSignal) {
	signal := &consumerLogSignal{message: message, seen: make(chan struct{})}
	handler := &consumerLogSignalHandler{
		base:   slog.NewTextHandler(io.Discard, nil),
		signal: signal,
	}
	return slog.New(handler), signal
}

func (h *consumerLogSignalHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *consumerLogSignalHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == h.signal.message {
		h.signal.once.Do(func() { close(h.signal.seen) })
	}
	return h.base.Handle(ctx, record)
}

func (h *consumerLogSignalHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &consumerLogSignalHandler{base: h.base.WithAttrs(attrs), signal: h.signal}
}

func (h *consumerLogSignalHandler) WithGroup(name string) slog.Handler {
	return &consumerLogSignalHandler{base: h.base.WithGroup(name), signal: h.signal}
}

func waitForConsumerLog(t *testing.T, signal *consumerLogSignal) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-signal.seen:
	case <-timer.C:
		t.Fatalf("consumer log did not report %q", signal.message)
	}
}

const dashboardStandDownMessage = "dashboard trigger consumer standing down; a resident consumer owns this home's queue. Retrying while it does."

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

func TestServeConsumerClaimsPendingWorkImmediately(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	seedSubmission(t, st, "run-ready", "missing-pipeline", "")

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result <- ServeConsumer(ctx, ConsumerOptions{
			Home: home, Store: st, Logger: quietLogger(), IdleTimeout: -1, Ready: ready,
		})
	}()
	t.Cleanup(func() {
		cancel()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			t.Error("consumer did not stop")
		}
	})
	startupTimer := time.NewTimer(time.Second)
	defer startupTimer.Stop()
	select {
	case <-ready:
	case err := <-result:
		t.Fatalf("consumer exited before readiness: %v", err)
	case <-startupTimer.C:
		t.Fatal("consumer did not become ready")
	}

	waitFor(t, "the startup consumer to claim pending work", 400*time.Millisecond, func() bool {
		run, err := st.GetRun(context.Background(), "run-ready")
		return err == nil && run.Status == "failed"
	})
}

func TestConsumerRunning_FalseBeforeAnyConsumerHasRun(t *testing.T) {
	running, err := ConsumerRunning(t.TempDir())
	if err != nil {
		t.Fatalf("ConsumerRunning on a fresh home: %v", err)
	}
	if running {
		t.Fatal("a home with no lock file reports a running consumer")
	}
}

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

	logger, stoodDown := newConsumerLogSignal(dashboardStandDownMessage)
	if err := RunLocalTriggerConsumer(ctx, home, st, logger); err != nil {
		t.Fatalf("dashboard consumer startup: %v", err)
	}
	waitForConsumerLog(t, stoodDown)
	pid, ok := ConsumerPID(home)
	if !ok || pid != residentPID {
		t.Fatalf("consumer pid = %d (ok=%v), want the resident %d still owning the queue", pid, ok, residentPID)
	}
}

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

func TestShouldExit_StaysWhenTheIdleWindowHasNotElapsed(t *testing.T) {
	st := consumerTestStore(t, t.TempDir())
	rt := consumerRuntime{idle: time.Hour, unlock: func() {}, relock: func() bool { return true }}
	if rt.shouldExit(context.Background(), st, quietLogger(), time.Now()) {
		t.Fatal("consumer exited before its idle window elapsed")
	}
}

func TestShouldExit_NeverExitsWhenIdleIsDisabled(t *testing.T) {
	st := consumerTestStore(t, t.TempDir())
	rt := consumerRuntime{idle: -1}
	if rt.shouldExit(context.Background(), st, quietLogger(), time.Now().Add(-24*time.Hour)) {
		t.Fatal("a consumer with idle exit disabled decided to exit")
	}
}

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

func TestConsumer_CancelRequestedBeforeClaimNeverDispatches(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-cancelme", "never-runs", "")

	claimed, err := st.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RequestCancel(ctx, claimed.ID); err != nil {
		t.Fatal(err)
	}

	if !cancelClaimedTriggerIfRequested(ctx, st, claimed, home, time.Minute, quietLogger()) {
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

func TestConsumer_NoCancelRequestedDispatchesNormally(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-normal", "runs-fine", "")
	claimed, err := st.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cancelClaimedTriggerIfRequested(ctx, st, claimed, home, time.Minute, quietLogger()) {
		t.Fatal("an uncancelled claim was treated as cancelled")
	}
}

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

func TestSubmittedTriggerRepoDir_AbsentOnOrdinaryTriggers(t *testing.T) {
	dir, err := submittedTriggerRepoDir(&store.Trigger{ID: "run-1", Pipeline: "lint"})
	if err != nil || dir != "" {
		t.Fatalf("submittedTriggerRepoDir = %q, %v; want empty and no error", dir, err)
	}
}

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

func TestSweeper_LeavesALiveRunningDispatchAlone(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-alive", "deploy", "")

	if _, err := st.ClaimNextTrigger(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-alive", Pipeline: "deploy", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

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

func TestSweeper_NeverRequeuesWhatThisConsumerIsExecuting(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx := context.Background()
	seedSubmission(t, st, "run-mine", "deploy", "")
	if _, err := st.ClaimNextTrigger(ctx, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	expireTriggerLease(t, st, "run-mine")

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

func TestDashboardConsumer_RetakesTheQueueAfterTheResidentIdlesOut(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	ctx, cancel := context.WithCancel(context.Background())

	ready := make(chan struct{})
	residentDone := make(chan error, 1)
	residentFinished := make(chan struct{})
	var dashboardFinished <-chan struct{}
	t.Cleanup(func() {
		cancel()
		for name, finished := range map[string]<-chan struct{}{
			"resident":  residentFinished,
			"dashboard": dashboardFinished,
		} {
			if finished == nil {
				continue
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-finished:
			case <-timer.C:
				t.Errorf("%s consumer did not stop", name)
			}
			timer.Stop()
		}
	})
	go func() {
		defer close(residentFinished)
		residentDone <- ServeConsumer(ctx, ConsumerOptions{
			Home: home, Store: st, Logger: quietLogger(),
			IdleTimeout: 300 * time.Millisecond, Ready: ready,
		})
	}()
	<-ready

	logger, stoodDown := newConsumerLogSignal(dashboardStandDownMessage)
	var err error
	dashboardFinished, err = runLocalTriggerConsumerWithRetryInterval(ctx, home, st, logger, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	waitForConsumerLog(t, stoodDown)

	select {
	case <-residentDone:
	case <-time.After(15 * time.Second):
		t.Fatal("resident consumer never idled out")
	}

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

	if err := st.FinishTrigger(ctx, "run-hb"); err != nil {
		t.Fatal(err)
	}
	if heartbeatOnce(ctx, st, "run-hb", claimed.ClaimSeq, 30*time.Second, 50*time.Millisecond, quietLogger()) {
		t.Fatal("heartbeat kept defending a claim that no longer exists")
	}
}

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
