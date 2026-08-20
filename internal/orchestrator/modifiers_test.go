package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
	sparkwing.Job(plan, "flaky", retryOKState.step()).Retry(3)
	return nil
}

type retryExhausted struct{ sparkwing.Base }

var retryExhaustedState = &flakyState{succeedAfter: 99}

func (retryExhausted) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "always-fails", retryExhaustedState.step()).Retry(2)
	return nil
}

type timeoutPipe struct{ sparkwing.Base }

func (timeoutPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "slow", func(ctx context.Context) error {
		select {
		case <-time.After(2 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}).Timeout(50 * time.Millisecond)
	return nil
}

type noProgressTimeoutPipe struct{ sparkwing.Base }

func (noProgressTimeoutPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "silent", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}).NoProgressTimeout(80 * time.Millisecond)
	return nil
}

type progressingPipe struct{ sparkwing.Base }

func (progressingPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "moving", func(ctx context.Context) error {
		for range 5 {
			time.Sleep(30 * time.Millisecond)
			sparkwing.Info(ctx, "processed batch")
		}
		return nil
	}).NoProgressTimeout(80 * time.Millisecond).Timeout(time.Second)
	return nil
}

type absoluteTimeoutWithProgressPipe struct{ sparkwing.Base }

func (absoluteTimeoutWithProgressPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "chatty", func(ctx context.Context) error {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sparkwing.Info(ctx, "still working")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}).NoProgressTimeout(60 * time.Millisecond).Timeout(180 * time.Millisecond)
	return nil
}

type noProgressRetryPipe struct{ sparkwing.Base }

var noProgressRetryAttempts atomic.Int32

func (noProgressRetryPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "recovering", func(ctx context.Context) error {
		if noProgressRetryAttempts.Add(1) == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}).Retry(1).NoProgressTimeout(60 * time.Millisecond)
	return nil
}

type noProgressLateActionPipe struct{ sparkwing.Base }

var noProgressLateActionStarted = make(chan context.Context, 1)

func (noProgressLateActionPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "late-action", func(ctx context.Context) error {
		noProgressLateActionStarted <- ctx
		<-ctx.Done()
		return nil
	}).NoProgressTimeout(50 * time.Millisecond)
	return nil
}

type noProgressLateVerifyPipe struct{ sparkwing.Base }

var noProgressLateVerifyStarted = make(chan context.Context, 1)

func (noProgressLateVerifyPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "late-verify", func(ctx context.Context) error { return nil }).
		Verify(func(ctx context.Context) error {
			noProgressLateVerifyStarted <- ctx
			<-ctx.Done()
			return nil
		}).
		NoProgressTimeout(50 * time.Millisecond)
	return nil
}

type absoluteLateActionPipe struct{ sparkwing.Base }

func (absoluteLateActionPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "late-action", func(ctx context.Context) error {
		time.Sleep(120 * time.Millisecond)
		return nil
	}).Timeout(50 * time.Millisecond)
	return nil
}

type onFailurePipe struct{ sparkwing.Base }

var rollbackCalled atomic.Bool

func (onFailurePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		return errors.New("deploy failed")
	}).OnFailure("rollback", func(ctx context.Context) error {
		rollbackCalled.Store(true)
		sparkwing.Info(ctx, "rollback fired")
		return nil
	})
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
	sparkwing.Step(w, "run", j.run)
	return nil, nil
}

func (detachedRollbackJob) run(ctx context.Context) error {
	detachedRecoveryCalled.Store(true)
	sparkwing.Info(ctx, "detached rollback fired")
	return nil
}

func (onFailureDetachedPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		return errors.New("deploy failed")
	}).OnFailure("detached-rollback", &detachedRollbackJob{})
	return nil
}

type onFailureSkipPipe struct{ sparkwing.Base }

var skipRollbackCalled atomic.Bool

func (onFailureSkipPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		return nil
	}).OnFailure("rollback", func(ctx context.Context) error {
		skipRollbackCalled.Store(true)
		return nil
	})
	return nil
}

func init() {
	register("mod-retry-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &retryOK{} })
	register("mod-retry-exhausted", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &retryExhausted{} })
	register("mod-timeout", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &timeoutPipe{} })
	register("mod-no-progress-timeout", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &noProgressTimeoutPipe{} })
	register("mod-progressing", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &progressingPipe{} })
	register("mod-absolute-timeout-with-progress", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &absoluteTimeoutWithProgressPipe{} })
	register("mod-no-progress-retry", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &noProgressRetryPipe{} })
	register("mod-no-progress-late-action", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &noProgressLateActionPipe{} })
	register("mod-no-progress-late-verify", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &noProgressLateVerifyPipe{} })
	register("mod-absolute-late-action", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &absoluteLateActionPipe{} })
	register("mod-onfailure", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onFailurePipe{} })
	register("mod-onfailure-skip", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onFailureSkipPipe{} })
	register("mod-onfailure-detached", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onFailureDetachedPipe{} })
}

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
	if got != 3 {
		t.Fatalf("attempts = %d, want 3 total", got)
	}
}

func TestRetry_LogCapturesAttempts(t *testing.T) {
	atomic.StoreInt32(&retryExhaustedState.attempts, 0)
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-retry-exhausted"})

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	if len(nodes) == 0 || nodes[0].NodeID != "always-fails" {
		t.Fatalf("expected always-fails node, got %+v", nodes)
	}

	logPath := p.NodeLog(res.RunID, "always-fails")
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
	if elapsed > 1*time.Second {
		t.Fatalf("run took %s; timeout should have cancelled much sooner", elapsed)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	if len(nodes) != 1 || !strings.Contains(nodes[0].Error, "timeout exceeded") {
		t.Fatalf("expected timeout error, got %+v", nodes)
	}
}

func TestNoProgressTimeout_CancelsSilentJob(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-no-progress-timeout"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].FailureReason != store.FailureNoProgressTimeout {
		t.Fatalf("failure reason = %+v, want %q", nodes, store.FailureNoProgressTimeout)
	}
	if !strings.Contains(nodes[0].Error, "no progress for 80ms") {
		t.Fatalf("error = %q, want no-progress duration", nodes[0].Error)
	}
}

func TestNoProgressTimeout_ResetsOnObservableProgress(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-progressing"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
}

func TestTimeout_RemainsAbsoluteWhileProgressContinues(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-absolute-timeout-with-progress"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].FailureReason != store.FailureTimeout {
		t.Fatalf("failure reason = %+v, want %q", nodes, store.FailureTimeout)
	}
}

func TestNoProgressTimeout_RetryStartsWithAFreshWindow(t *testing.T) {
	noProgressRetryAttempts.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "mod-no-progress-retry"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
	if got := noProgressRetryAttempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestNoProgressTimeout_RejectsLateActionSuccess(t *testing.T) {
	assertForcedNoProgressTimeout(t, "mod-no-progress-late-action", noProgressLateActionStarted)
}

func TestNoProgressTimeout_RejectsLateVerifierSuccess(t *testing.T) {
	assertForcedNoProgressTimeout(t, "mod-no-progress-late-verify", noProgressLateVerifyStarted)
}

func TestTimeout_RejectsLateActionSuccess(t *testing.T) {
	assertTimeoutReason(t, "mod-absolute-late-action", store.FailureTimeout)
}

func assertTimeoutReason(t *testing.T, pipeline, wantReason string) {
	t.Helper()
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: pipeline})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].FailureReason != wantReason {
		t.Fatalf("failure reason = %+v, want %q", nodes, wantReason)
	}
}

func assertForcedNoProgressTimeout(t *testing.T, pipeline string, started <-chan context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	type runResult struct {
		result *orchestrator.Result
		err    error
	}
	resultCh := make(chan runResult, 1)
	finished := make(chan struct{})
	p := newPaths(t)
	go func() {
		defer close(finished)
		result, err := orchestrator.RunLocal(ctx, p, orchestrator.Options{Pipeline: pipeline})
		resultCh <- runResult{result: result, err: err}
	}()
	t.Cleanup(func() {
		cancel()
		joinTimer := time.NewTimer(time.Second)
		defer joinTimer.Stop()
		select {
		case <-finished:
		case <-joinTimer.C:
			t.Errorf("%s did not stop after cancellation", pipeline)
		}
	})

	var attemptCtx context.Context
	select {
	case attemptCtx = <-started:
	case <-ctx.Done():
		t.Fatalf("%s did not start its late-success callback: %v", pipeline, ctx.Err())
	}
	if !orchestrator.ExpireProgressTimeoutForTest(attemptCtx) {
		t.Fatalf("%s callback has no active progress timeout", pipeline)
	}

	var run runResult
	select {
	case run = <-resultCh:
	case <-ctx.Done():
		t.Fatalf("%s did not finish after forced timeout: %v", pipeline, ctx.Err())
	}
	if run.err != nil {
		t.Fatalf("Run: %v", run.err)
	}
	if run.result == nil || run.result.Status != "failed" {
		t.Fatalf("result = %+v, want failed", run.result)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	nodes, err := st.ListNodes(context.Background(), run.result.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].FailureReason != store.FailureNoProgressTimeout {
		t.Fatalf("failure reason = %+v, want %q", nodes, store.FailureNoProgressTimeout)
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
	defer func() { _ = st.Close() }()
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
	defer func() { _ = st.Close() }()
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
	defer func() { _ = st.Close() }()
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
