package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type flakyState struct {
	attempts     int32
	succeedAfter int32
}

func (state *flakyState) step() func(ctx context.Context) error {
	return func(ctx context.Context) error {
		attempt := atomic.AddInt32(&state.attempts, 1)
		if attempt <= state.succeedAfter {
			return errors.New("transient")
		}
		return nil
	}
}

type retryOK struct{ sparkwing.Base }

var retryOKState = &flakyState{succeedAfter: 2}

func (retryOK) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "flaky", retryOKState.step()).Retry(3)
	return nil
}

type retryExhausted struct{ sparkwing.Base }

var retryExhaustedState = &flakyState{succeedAfter: 99}

func (retryExhausted) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "always-fails", retryExhaustedState.step()).Retry(2)
	return nil
}

type timeoutPipe struct {
	sparkwing.Base
	timeout time.Duration
}

type observableTimeoutGate struct {
	complete chan struct{}
	returned chan error
}

var timeoutTestGate *observableTimeoutGate

func (pipeline timeoutPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	gate := timeoutTestGate
	sparkwing.Job(plan, "slow", func(ctx context.Context) error {
		var err error
		select {
		case <-gate.complete:
		case <-ctx.Done():
			err = ctx.Err()
		}
		gate.returned <- err
		return err
	}).Timeout(pipeline.timeout)
	return nil
}

type noProgressTimeoutPipe struct{ sparkwing.Base }

func (noProgressTimeoutPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "silent", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}).NoProgressTimeout(80 * time.Millisecond)
	return nil
}

type progressingPipe struct{ sparkwing.Base }

type observableProgressGate struct {
	started    chan context.Context
	emit       chan struct{}
	emitted    chan struct{}
	finish     chan struct{}
	emitOnce   sync.Once
	finishOnce sync.Once
}

func newObservableProgressGate() *observableProgressGate {
	return &observableProgressGate{
		started: make(chan context.Context, 1),
		emit:    make(chan struct{}),
		emitted: make(chan struct{}),
		finish:  make(chan struct{}),
	}
}

func (gate *observableProgressGate) emitProgress() {
	gate.emitOnce.Do(func() { close(gate.emit) })
}

func (gate *observableProgressGate) finishJob() {
	gate.finishOnce.Do(func() { close(gate.finish) })
}

var progressingTestGate *observableProgressGate

func (progressingPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "moving", func(ctx context.Context) error {
		gate := progressingTestGate
		gate.started <- ctx
		select {
		case <-gate.emit:
		case <-ctx.Done():
			return ctx.Err()
		}
		sparkwing.Info(ctx, "processed batch")
		close(gate.emitted)
		select {
		case <-gate.finish:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}).NoProgressTimeout(time.Hour).Timeout(time.Hour)
	return nil
}

type absoluteTimeoutWithProgressPipe struct{ sparkwing.Base }

func (absoluteTimeoutWithProgressPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "chatty", func(ctx context.Context) error {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sparkwing.Info(ctx, "still working")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		// SAFETY: The progress window absorbs scheduling stalls between progress events.
	}).NoProgressTimeout(400 * time.Millisecond).Timeout(time.Second)
	return nil
}

type noProgressRetryPipe struct{ sparkwing.Base }

var noProgressRetryAttempts atomic.Int32

func (noProgressRetryPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
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

func (noProgressLateActionPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "late-action", func(ctx context.Context) error {
		noProgressLateActionStarted <- ctx
		<-ctx.Done()
		return nil
	}).NoProgressTimeout(time.Hour)
	return nil
}

type noProgressLateVerifyPipe struct{ sparkwing.Base }

var noProgressLateVerifyStarted = make(chan context.Context, 1)

func (noProgressLateVerifyPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "late-verify", func(ctx context.Context) error { return nil }).
		Verify(func(ctx context.Context) error {
			noProgressLateVerifyStarted <- ctx
			<-ctx.Done()
			return nil
		}).
		NoProgressTimeout(time.Hour)
	return nil
}

type absoluteLateActionPipe struct{ sparkwing.Base }

var absoluteLateActionStarted = make(chan context.Context, 1)

func (absoluteLateActionPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "late-action", func(ctx context.Context) error {
		absoluteLateActionStarted <- ctx
		<-ctx.Done()
		return nil
	}).Timeout(time.Hour)
	return nil
}

type onFailurePipe struct{ sparkwing.Base }

var rollbackCalled atomic.Bool

func (onFailurePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		return errors.New("deploy failed")
	}).OnFailure("rollback", func(ctx context.Context) error {
		rollbackCalled.Store(true)
		sparkwing.Info(ctx, "rollback fired")
		return nil
	})
	return nil
}

type onFailureDetachedPipe struct{ sparkwing.Base }

var detachedRecoveryCalled atomic.Bool

type detachedRollbackJob struct{ sparkwing.Base }

func (job *detachedRollbackJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run)
	return nil, nil
}

func (detachedRollbackJob) run(ctx context.Context) error {
	detachedRecoveryCalled.Store(true)
	sparkwing.Info(ctx, "detached rollback fired")
	return nil
}

func (onFailureDetachedPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		return errors.New("deploy failed")
	}).OnFailure("detached-rollback", &detachedRollbackJob{})
	return nil
}

type onFailureSkipPipe struct{ sparkwing.Base }

var skipRollbackCalled atomic.Bool

func (onFailureSkipPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
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
	register("mod-timeout", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &timeoutPipe{timeout: 50 * time.Millisecond} })
	register("mod-without-timeout", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &timeoutPipe{} })
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
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "mod-retry-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("status = %q, want success", result.Status)
	}
	got := atomic.LoadInt32(&retryOKState.attempts)
	if got != 3 {
		t.Fatalf("attempts = %d, want 3 (2 fails + 1 success)", got)
	}
}

func TestRetry_ExhaustedStillFails(t *testing.T) {
	atomic.StoreInt32(&retryExhaustedState.attempts, 0)
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "mod-retry-exhausted"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	got := atomic.LoadInt32(&retryExhaustedState.attempts)
	if got != 3 {
		t.Fatalf("attempts = %d, want 3 total", got)
	}
}

func TestRetry_LogCapturesAttempts(t *testing.T) {
	atomic.StoreInt32(&retryExhaustedState.attempts, 0)
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "mod-retry-exhausted"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		if err := state.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	nodes, err := state.ListNodes(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) == 0 || nodes[0].NodeID != "always-fails" {
		t.Fatalf("expected always-fails node, got %+v", nodes)
	}

	logPath := paths.NodeLog(result.RunID, "always-fails")
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
	for _, test := range []struct {
		name              string
		pipeline          string
		complete          bool
		wantStatus        string
		wantFailureReason string
		wantError         error
	}{
		{"deadline cancels body", "mod-timeout", false, "failed", store.FailureTimeout, context.DeadlineExceeded},
		{"body completes without timeout", "mod-without-timeout", true, "success", "", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			gate := &observableTimeoutGate{complete: make(chan struct{}), returned: make(chan error, 1)}
			timeoutTestGate = gate
			if test.complete {
				close(gate.complete)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			paths := newPaths(t)
			result, err := orchestrator.RunLocal(ctx, paths, orchestrator.Options{Pipeline: test.pipeline})
			if err != nil {
				t.Fatalf("RunLocal: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatalf("run exhausted the test deadline: %v", ctx.Err())
			}
			select {
			case bodyError := <-gate.returned:
				if !errors.Is(bodyError, test.wantError) {
					t.Fatalf("body error = %v, want %v", bodyError, test.wantError)
				}
			default:
				t.Fatal("run finished without the job body returning")
			}
			if result.Status != test.wantStatus {
				t.Fatalf("run status = %q, want %q", result.Status, test.wantStatus)
			}
			state, err := store.Open(paths.StateDB())
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer func() {
				if err := state.Close(); err != nil {
					t.Errorf("close store: %v", err)
				}
			}()
			nodes, err := state.ListNodes(context.Background(), result.RunID)
			if err != nil {
				t.Fatalf("list nodes: %v", err)
			}
			if len(nodes) != 1 || nodes[0].Outcome != test.wantStatus || nodes[0].FailureReason != test.wantFailureReason {
				t.Fatalf("nodes = %+v, want one %s node with failure reason %q", nodes, test.wantStatus, test.wantFailureReason)
			}
			if test.wantError != nil && !strings.Contains(nodes[0].Error, "timeout exceeded") {
				t.Fatalf("node error = %q, want timeout exceeded", nodes[0].Error)
			}
		})
	}
}

func TestNoProgressTimeout_CancelsSilentJob(t *testing.T) {
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "mod-no-progress-timeout"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed", result.Status)
	}

	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		if err := state.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	nodes, err := state.ListNodes(context.Background(), result.RunID)
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
	gate := newObservableProgressGate()
	progressingTestGate = gate
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	type runResult struct {
		result *orchestrator.Result
		err    error
	}
	results := make(chan runResult, 1)
	finished := make(chan struct{})
	paths := newPaths(t)
	go func() {
		defer close(finished)
		result, err := orchestrator.RunLocal(ctx, paths, orchestrator.Options{Pipeline: "mod-progressing"})
		results <- runResult{result: result, err: err}
	}()
	t.Cleanup(func() {
		gate.emitProgress()
		gate.finishJob()
		cancel()
		joinTimer := time.NewTimer(time.Second)
		defer joinTimer.Stop()
		select {
		case <-finished:
		case <-joinTimer.C:
			t.Error("progress-reset run did not stop after cancellation")
		}
	})

	var attemptContext context.Context
	select {
	case attemptContext = <-gate.started:
	case <-ctx.Done():
		t.Fatalf("progressing job did not start: %v", ctx.Err())
	}
	generation, ok := orchestrator.ProgressTimeoutGenerationForTest(attemptContext)
	if !ok {
		t.Fatal("progressing job has no active progress timeout")
	}
	gate.emitProgress()
	select {
	case <-gate.emitted:
	case <-ctx.Done():
		t.Fatalf("progressing job did not emit progress: %v", ctx.Err())
	}
	if orchestrator.ExpireProgressTimeoutGenerationForTest(attemptContext, generation) {
		t.Fatal("logged progress did not invalidate the prior timeout generation")
	}
	select {
	case <-attemptContext.Done():
		t.Fatalf("progressing job context ended after stale timeout expiry: %v", attemptContext.Err())
	default:
	}
	gate.finishJob()

	var run runResult
	select {
	case run = <-results:
	case <-ctx.Done():
		t.Fatalf("progressing job did not finish: %v", ctx.Err())
	}
	if run.err != nil {
		t.Fatalf("Run: %v", run.err)
	}
	if run.result == nil || run.result.Status != "success" {
		t.Fatalf("result = %+v, want success", run.result)
	}
}

func TestTimeout_RemainsAbsoluteWhileProgressContinues(t *testing.T) {
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "mod-absolute-timeout-with-progress"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed", result.Status)
	}

	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		if err := state.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	nodes, err := state.ListNodes(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].FailureReason != store.FailureTimeout {
		t.Fatalf("failure reason = %+v, want %q", nodes, store.FailureTimeout)
	}
}

func TestNoProgressTimeout_RetryStartsWithAFreshWindow(t *testing.T) {
	noProgressRetryAttempts.Store(0)
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "mod-no-progress-retry"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("status = %q, want success", result.Status)
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
	assertForcedAbsoluteTimeout(t, "mod-absolute-late-action", absoluteLateActionStarted)
}

func assertForcedNoProgressTimeout(t *testing.T, pipeline string, started <-chan context.Context) {
	t.Helper()
	assertForcedTimeout(t, pipeline, started, orchestrator.ForceProgressTimeoutForTest, store.FailureNoProgressTimeout)
}

func assertForcedAbsoluteTimeout(t *testing.T, pipeline string, started <-chan context.Context) {
	t.Helper()
	assertForcedTimeout(t, pipeline, started, orchestrator.ForceNodeTimeoutForTest, store.FailureTimeout)
}

func assertForcedTimeout(t *testing.T, pipeline string, started <-chan context.Context, force func(context.Context) bool, wantFailureReason string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	type runResult struct {
		result *orchestrator.Result
		err    error
	}
	results := make(chan runResult, 1)
	finished := make(chan struct{})
	paths := newPaths(t)
	go func() {
		defer close(finished)
		result, err := orchestrator.RunLocal(ctx, paths, orchestrator.Options{Pipeline: pipeline})
		results <- runResult{result: result, err: err}
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

	var attemptContext context.Context
	select {
	case attemptContext = <-started:
	case <-ctx.Done():
		t.Fatalf("%s did not start its late-success callback: %v", pipeline, ctx.Err())
	}
	if !force(attemptContext) {
		t.Fatalf("%s callback timeout was not active", pipeline)
	}

	var run runResult
	select {
	case run = <-results:
	case <-ctx.Done():
		t.Fatalf("%s did not finish after forced timeout: %v", pipeline, ctx.Err())
	}
	if run.err != nil {
		t.Fatalf("Run: %v", run.err)
	}
	if run.result == nil || run.result.Status != "failed" {
		t.Fatalf("result = %+v, want failed", run.result)
	}
	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		if err := state.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	nodes, err := state.ListNodes(context.Background(), run.result.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].FailureReason != wantFailureReason {
		t.Fatalf("failure reason = %+v, want %q", nodes, wantFailureReason)
	}
}

func TestOnFailure_RunsWhenParentFails(t *testing.T) {
	rollbackCalled.Store(false)
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "mod-onfailure"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	if result.Status != "failed" {
		t.Fatalf("run status = %q, want failed", result.Status)
	}
	if !rollbackCalled.Load() {
		t.Fatal("rollback was not called")
	}

	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		if err := state.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	nodes, err := state.ListNodes(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	nodesByID := map[string]*store.Node{}
	for _, node := range nodes {
		nodesByID[node.NodeID] = node
	}
	if nodesByID["deploy"].Outcome != string(sparkwing.Failed) {
		t.Fatalf("deploy outcome = %q", nodesByID["deploy"].Outcome)
	}
	if nodesByID["rollback"].Outcome != string(sparkwing.Success) {
		t.Fatalf("rollback outcome = %q, want success", nodesByID["rollback"].Outcome)
	}
}

func TestOnFailure_SkippedWhenParentSucceeds(t *testing.T) {
	skipRollbackCalled.Store(false)
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "mod-onfailure-skip"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	if result.Status != "success" {
		t.Fatalf("run status = %q, want success", result.Status)
	}
	if skipRollbackCalled.Load() {
		t.Fatal("rollback ran after its parent succeeded")
	}

	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		if err := state.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	nodes, err := state.ListNodes(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	nodesByID := map[string]*store.Node{}
	for _, node := range nodes {
		nodesByID[node.NodeID] = node
	}
	if nodesByID["rollback"].Outcome != string(sparkwing.Skipped) {
		t.Fatalf("rollback outcome = %q, want skipped", nodesByID["rollback"].Outcome)
	}
	if !strings.Contains(nodesByID["rollback"].Error, "did not fail") {
		t.Fatalf("rollback reason = %q", nodesByID["rollback"].Error)
	}
}

func TestOnFailure_DetachedRecoveryRuns(t *testing.T) {
	detachedRecoveryCalled.Store(false)
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "mod-onfailure-detached"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}

	if result.Status != "failed" {
		t.Fatalf("run status = %q, want failed", result.Status)
	}
	if !detachedRecoveryCalled.Load() {
		t.Fatal("detached recovery was not called")
	}

	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		if err := state.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	nodes, err := state.ListNodes(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	nodesByID := map[string]*store.Node{}
	for _, node := range nodes {
		nodesByID[node.NodeID] = node
	}
	if nodesByID["deploy"] == nil {
		t.Fatalf("deploy node missing from store: %+v", nodes)
	}
	if nodesByID["deploy"].Outcome != string(sparkwing.Failed) {
		t.Fatalf("deploy outcome = %q, want failed", nodesByID["deploy"].Outcome)
	}
	if nodesByID["detached-rollback"] == nil {
		t.Fatalf("detached-rollback node missing from store: %+v", nodes)
	}
	if nodesByID["detached-rollback"].Outcome != string(sparkwing.Success) {
		t.Fatalf("detached-rollback outcome = %q, want success", nodesByID["detached-rollback"].Outcome)
	}
}

func readFile(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(contents), nil
}
