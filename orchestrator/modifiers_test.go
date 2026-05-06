package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// --- Retry ---

// flakyState counts invocations of a closure so tests can assert on
// retry behavior without racy sleeps. One instance per registered
// pipeline (orch.Register is global).
type flakyState struct {
	attempts     int32
	succeedAfter int32
}

func (f *flakyState) step() func(ctx context.Context) error {
	return func(ctx context.Context) error {
		cur := atomic.AddInt32(&f.attempts, 1)
		if cur <= f.succeedAfter {
			return errors.New("transient")
		}
		return nil
	}
}

type retryOK struct{ sparkwing.Base }

var retryOKState = &flakyState{succeedAfter: 2}

func (retryOK) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "flaky", sparkwing.JobFn(retryOKState.step())).Retry(3)
	return nil
}

type retryExhausted struct{ sparkwing.Base }

var retryExhaustedState = &flakyState{succeedAfter: 99}

func (retryExhausted) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "always-fails", sparkwing.JobFn(retryExhaustedState.step())).Retry(2)
	return nil
}

// --- Timeout ---

type timeoutPipe struct{ sparkwing.Base }

func (timeoutPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "slow", sparkwing.JobFn(func(ctx context.Context) error {
		select {
		case <-time.After(2 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})).Timeout(50 * time.Millisecond)
	return nil
}

// --- OnFailure ---

type onFailurePipe struct{ sparkwing.Base }

var rollbackCalled atomic.Bool

func (onFailurePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", sparkwing.JobFn(func(ctx context.Context) error {
		return errors.New("deploy failed")
	})).OnFailure("rollback", sparkwing.JobFn(func(ctx context.Context) error {
		rollbackCalled.Store(true)
		sparkwing.Info(ctx, "rollback fired")
		return nil
	}))
	return nil
}

// onFailureDetachedPipe exercises the "detached recovery" shape:
// the recovery node is constructed inside .OnFailure(id, job) and
// is not in plan.Nodes(). Regression guard: dispatch must still
// schedule the recovery goroutine so it fires when the parent
// fails.
type onFailureDetachedPipe struct{ sparkwing.Base }

var detachedRecoveryCalled atomic.Bool

type detachedRollbackJob struct{ sparkwing.Base }

func (j *detachedRollbackJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.Step("run", j.run)
	return nil, nil
}

func (detachedRollbackJob) run(ctx context.Context) error {
	detachedRecoveryCalled.Store(true)
	sparkwing.Info(ctx, "detached rollback fired")
	return nil
}

func (onFailureDetachedPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", sparkwing.JobFn(func(ctx context.Context) error {
		return errors.New("deploy failed")
	})).OnFailure("detached-rollback", &detachedRollbackJob{})
	return nil
}

type onFailureSkipPipe struct{ sparkwing.Base }

var skipRollbackCalled atomic.Bool

func (onFailureSkipPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", sparkwing.JobFn(func(ctx context.Context) error {
		return nil // succeeds
	})).OnFailure("rollback", sparkwing.JobFn(func(ctx context.Context) error {
		skipRollbackCalled.Store(true)
		return nil
	}))
	return nil
}

func init() {
	register("mod-retry-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &retryOK{} })
	register("mod-retry-exhausted", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &retryExhausted{} })
	register("mod-timeout", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &timeoutPipe{} })
	register("mod-onfailure", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onFailurePipe{} })
	register("mod-onfailure-skip", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onFailureSkipPipe{} })
	register("mod-onfailure-detached", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onFailureDetachedPipe{} })
}

// --- Tests ---

func TestRetry_EventuallySucceeds(t *testing.T) {
	atomic.StoreInt32(&retryOKState.attempts, 0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-retry-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
	got := atomic.LoadInt32(&retryOKState.attempts)
	if got != 3 {
		t.Fatalf("attempts = %d, want 3 (2 fails + 1 success)", got)
	}
}

func TestRetry_ExhaustedStillFails(t *testing.T) {
	atomic.StoreInt32(&retryExhaustedState.attempts, 0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-retry-exhausted"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	got := atomic.LoadInt32(&retryExhaustedState.attempts)
	// Retry(2) means total of 3 attempts (1 original + 2 retries).
	if got != 3 {
		t.Fatalf("attempts = %d, want 3 total", got)
	}
}

func TestRetry_LogCapturesAttempts(t *testing.T) {
	atomic.StoreInt32(&retryExhaustedState.attempts, 0)
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-retry-exhausted"})

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	if len(nodes) == 0 || nodes[0].NodeID != "always-fails" {
		t.Fatalf("expected always-fails node, got %+v", nodes)
	}

	// Log file should contain retry banners for attempts 2 and 3.
	logPath := p.NodeLog(res.RunID, "always-fails")
	// Read via jobs-logs API which knows the path layout.
	// (Direct read is simpler here — use sparkwing's utils wouldn't help.)
	body, err := readFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(body, "retry attempt 2/3") {
		t.Fatalf("log missing retry banner: %s", body)
	}
	if !strings.Contains(body, "retry attempt 3/3") {
		t.Fatalf("log missing final retry banner: %s", body)
	}
}

func TestTimeout_CancelsSlowJob(t *testing.T) {
	p := newPaths(t)
	start := time.Now()
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-timeout"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	// Timeout is 50ms -- should finish well under the job's 2s sleep.
	if elapsed > 1*time.Second {
		t.Fatalf("run took %s; timeout should have cancelled much sooner", elapsed)
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	if len(nodes) != 1 || !strings.Contains(nodes[0].Error, "timeout exceeded") {
		t.Fatalf("expected timeout error, got %+v", nodes)
	}
}

func TestOnFailure_RunsWhenParentFails(t *testing.T) {
	rollbackCalled.Store(false)
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-onfailure"})

	if res.Status != "failed" {
		t.Fatalf("run status should still be failed (parent failed): got %q", res.Status)
	}
	if !rollbackCalled.Load() {
		t.Fatal("rollback was not called")
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	byID := map[string]*store.Node{}
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	if byID["deploy"].Outcome != string(sparkwing.Failed) {
		t.Fatalf("deploy outcome = %q", byID["deploy"].Outcome)
	}
	if byID["rollback"].Outcome != string(sparkwing.Success) {
		t.Fatalf("rollback outcome = %q, want success", byID["rollback"].Outcome)
	}
}

func TestOnFailure_SkippedWhenParentSucceeds(t *testing.T) {
	skipRollbackCalled.Store(false)
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-onfailure-skip"})

	if res.Status != "success" {
		t.Fatalf("run status = %q, want success", res.Status)
	}
	if skipRollbackCalled.Load() {
		t.Fatal("rollback should NOT run when parent succeeds")
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	byID := map[string]*store.Node{}
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	if byID["rollback"].Outcome != string(sparkwing.Skipped) {
		t.Fatalf("rollback outcome = %q, want skipped", byID["rollback"].Outcome)
	}
	if !strings.Contains(byID["rollback"].Error, "did not fail") {
		t.Fatalf("rollback reason = %q", byID["rollback"].Error)
	}
}

// TestOnFailure_DetachedRecoveryRuns verifies that a recovery node
// attached via .OnFailure(id, job) -- the recovery is constructed
// detached and is NOT in plan.Nodes() -- is still scheduled by
// dispatch and fires when the parent fails. Previously dispatch
// only iterated plan.Nodes(), so detached recovery goroutines never
// started and the rollback silently didn't run.
func TestOnFailure_DetachedRecoveryRuns(t *testing.T) {
	detachedRecoveryCalled.Store(false)
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-onfailure-detached"})

	if res.Status != "failed" {
		t.Fatalf("run status = %q, want failed (parent failed)", res.Status)
	}
	if !detachedRecoveryCalled.Load() {
		t.Fatal("detached recovery was not called")
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	byID := map[string]*store.Node{}
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	if byID["deploy"] == nil {
		t.Fatalf("deploy node missing from store: %+v", nodes)
	}
	if byID["deploy"].Outcome != string(sparkwing.Failed) {
		t.Fatalf("deploy outcome = %q, want failed", byID["deploy"].Outcome)
	}
	if byID["detached-rollback"] == nil {
		t.Fatalf("detached-rollback node missing from store: %+v", nodes)
	}
	if byID["detached-rollback"].Outcome != string(sparkwing.Success) {
		t.Fatalf("detached-rollback outcome = %q, want success", byID["detached-rollback"].Outcome)
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
