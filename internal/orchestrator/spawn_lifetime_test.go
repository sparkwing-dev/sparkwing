package orchestrator

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type spawnLifetimeJob struct{ sparkwing.Base }

func (spawnLifetimeJob) Work(*sparkwing.Work) (*sparkwing.WorkStep, error) { return nil, nil }

type spawnLifetimeState struct{ StateBackend }

func (spawnLifetimeState) CreateNode(context.Context, store.Node) error { return nil }
func (spawnLifetimeState) AppendEvent(context.Context, string, string, string, []byte) error {
	return nil
}

func (spawnLifetimeState) FinishNode(context.Context, string, string, string, string, []byte) error {
	return nil
}

type spawnLifetimeRunner func(context.Context, runner.Request) runner.Result

func (f spawnLifetimeRunner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	return f(ctx, req)
}

type cancelledApprovalRead struct {
	StateBackend
	cancel   context.CancelFunc
	reads    int
	resolves int
}

func (s *cancelledApprovalRead) GetApproval(ctx context.Context, _, _ string) (*store.Approval, error) {
	s.reads++
	if s.reads == 1 {
		return &store.Approval{}, nil
	}
	s.cancel()
	return nil, ctx.Err()
}

func (s *cancelledApprovalRead) ResolveApproval(context.Context, string, string, string, string, string) (*store.Approval, error) {
	s.resolves++
	return nil, store.ErrLockHeld
}

func TestSpawnCancellationDuringApprovalConflictCannotApprove(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		state := &cancelledApprovalRead{cancel: cancel}
		s := &dispatchState{ctx: ctx, resolverCtx: ctx, runID: "run", backends: Backends{State: state}}
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		got := s.pollApproval(ctx, "child", time.Now().Add(-time.Minute), store.ApprovalOnTimeoutApprove, ticker)
		if got.outcome != sparkwing.Cancelled {
			t.Errorf("cancelled conflict reread produced %q, want cancelled", got.outcome)
		}
		if state.reads != 2 || state.resolves != 1 {
			t.Errorf("reads=%d resolves=%d, want one initial read, one conflict and one reread", state.reads, state.resolves)
		}
	})
}

type spawnRetryReads struct {
	spawnLifetimeState
	blockedAt int
	reads     int
	started   chan struct{}
}

func (s *spawnRetryReads) GetNode(ctx context.Context, _, _ string) (*store.Node, error) {
	s.reads++
	if s.reads == s.blockedAt {
		close(s.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &store.Node{}, nil
}

func TestSpawnCancellationInterruptsRetryReads(t *testing.T) {
	for _, blockedAt := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(blockedAt), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runCtx, cancelRun := context.WithCancel(context.Background())
				defer cancelRun()
				state := &spawnRetryReads{blockedAt: blockedAt, started: make(chan struct{})}
				attempts := 0
				r := spawnLifetimeRunner(func(context.Context, runner.Request) runner.Result {
					attempts++
					return runner.Result{Outcome: sparkwing.Failed, Err: errors.New("retryable failure")}
				})
				s := newDispatchState(runCtx, Backends{State: state}, r, "run", "parcels", sparkwing.NewPlan(), nil, DebugDirectives{}, "", nil, 1, nil, "", "", false)
				childCtx, cancelChild := context.WithCancel(s.resolverCtx)
				defer cancelChild()
				node := sparkwing.NewDetachedNode("child", spawnLifetimeJob{}).Retry(1, sparkwing.RetryAuto(), sparkwing.RetryBackoff(time.Hour))
				finished := make(chan struct{})
				go func() {
					s.runOneNode(childCtx, node)
					close(finished)
				}()
				<-state.started
				cancelChild()
				synctest.Wait()
				returned := false
				select {
				case <-finished:
					returned = true
					if outcome, ok := s.getOutcome("child"); !ok || outcome != sparkwing.Cancelled {
						t.Errorf("cancelled retry read produced outcome %q, present=%v", outcome, ok)
					}
				default:
					t.Error("child cancellation did not interrupt retry control read")
				}
				cancelRun()
				if !returned {
					<-finished
				}
				wantAttempts := 0
				if blockedAt == 3 {
					wantAttempts = 1
				}
				if attempts != wantAttempts {
					t.Errorf("attempts=%d, want %d", attempts, wantAttempts)
				}
			})
		})
	}
}

type spawnBlockingReads struct {
	spawnLifetimeState
	started chan struct{}
}

func (s spawnBlockingReads) GetActiveDebugPause(ctx context.Context, _, _ string) (*store.DebugPause, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s spawnBlockingReads) GetApproval(ctx context.Context, _, _ string) (*store.Approval, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (spawnBlockingReads) CreateDebugPause(context.Context, store.DebugPause) error { return nil }
func (spawnBlockingReads) SetNodeStatus(context.Context, string, string, string) error {
	return nil
}

func TestSpawnCancellationInterruptsBackendReads(t *testing.T) {
	for _, phase := range []string{"pause", "approval"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runCtx, cancelRun := context.WithCancel(context.Background())
				defer cancelRun()
				childCtx, cancelChild := context.WithCancel(runCtx)
				defer cancelChild()
				started := make(chan struct{}, 1)
				s := &dispatchState{ctx: runCtx, resolverCtx: runCtx, runID: "run", backends: Backends{State: spawnBlockingReads{started: started}}}
				result := make(chan bool, 1)
				go func() {
					if phase == "pause" {
						result <- s.doPause(childCtx, "child", store.PauseReasonBefore)
						return
					}
					ticker := time.NewTicker(time.Second)
					defer ticker.Stop()
					got := s.pollApproval(childCtx, "child", time.Time{}, store.ApprovalOnTimeoutFail, ticker)
					result <- got.outcome == sparkwing.Cancelled
				}()
				<-started
				cancelChild()
				synctest.Wait()
				returned := false
				select {
				case cancelled := <-result:
					returned = true
					if !cancelled {
						t.Error("cancelled backend read did not produce a cancelled outcome")
					}
				default:
					t.Error("child cancellation did not interrupt the backend read")
				}
				if runCtx.Err() != nil {
					t.Error("child cancellation cancelled the shared run")
				}
				cancelRun()
				if !returned {
					<-result
				}
			})
		})
	}
}

func TestSpawnAlreadyCancelledDoesNotExecuteChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runCtx := context.Background()
		parentCtx, cancelParent := context.WithCancel(runCtx)
		cancelParent()
		childRan := false
		r := spawnLifetimeRunner(func(context.Context, runner.Request) runner.Result {
			childRan = true
			return runner.Result{Outcome: sparkwing.Success}
		})
		s := newDispatchState(runCtx, Backends{State: spawnLifetimeState{}}, r, "run", "parcels", sparkwing.NewPlan(), nil, DebugDirectives{}, "", nil, 0, nil, "", "", false)
		_, err := s.newSpawnHandler("parent").Spawn(parentCtx, "parent", "child", spawnLifetimeJob{})
		s.wg.Wait()
		if childRan {
			t.Error("already-cancelled Spawn executed its child")
		}
		if err == nil {
			t.Error("already-cancelled Spawn returned success")
		}
	})
}

func TestSpawnCancellationJoinsChildCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runCtx, cancelRun := context.WithCancel(context.Background())
		defer cancelRun()
		parentCtx, cancelParent := context.WithCancel(runCtx)
		defer cancelParent()
		started := make(chan context.Context, 1)
		cleanup := make(chan struct{})
		childStopped := false
		r := spawnLifetimeRunner(func(ctx context.Context, _ runner.Request) runner.Result {
			started <- ctx
			<-ctx.Done()
			<-cleanup
			childStopped = true
			return runner.Result{Outcome: sparkwing.Cancelled}
		})
		s := newDispatchState(runCtx, Backends{State: spawnLifetimeState{}}, r, "run", "parcels", sparkwing.NewPlan(), nil, DebugDirectives{}, "", nil, 1, nil, "", "", false)
		result := make(chan error, 1)
		go func() {
			_, err := s.newSpawnHandler("parent").Spawn(parentCtx, "parent", "child", spawnLifetimeJob{})
			result <- err
		}()
		childCtx := <-started
		cancelParent()
		synctest.Wait()
		if childCtx.Err() == nil {
			t.Error("parent cancellation did not reach the child execution context")
		}
		returned := false
		select {
		case <-result:
			returned = true
			t.Error("Spawn returned while child cleanup was blocked")
		default:
		}
		cancelRun()
		close(cleanup)
		if !returned {
			if err := <-result; err == nil {
				t.Error("Spawn lost parent cancellation")
			}
		}
		s.wg.Wait()
		if !childStopped {
			t.Error("child cleanup did not finish")
		}
	})
}

func TestSpawnCancellationJoinsChildWaitingForWorkerSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runCtx, cancelRun := context.WithCancel(context.Background())
		defer cancelRun()
		parentCtx, cancelParent := context.WithCancel(runCtx)
		defer cancelParent()
		childRan := false
		r := spawnLifetimeRunner(func(context.Context, runner.Request) runner.Result {
			childRan = true
			return runner.Result{Outcome: sparkwing.Success}
		})
		s := newDispatchState(runCtx, Backends{State: spawnLifetimeState{}}, r, "run", "parcels", sparkwing.NewPlan(), nil, DebugDirectives{}, "", nil, 1, nil, "", "", false)
		s.sem <- struct{}{}
		result := make(chan error, 1)
		go func() {
			_, err := s.newSpawnHandler("parent").Spawn(parentCtx, "parent", "child", spawnLifetimeJob{})
			result <- err
		}()
		synctest.Wait()
		cancelParent()
		synctest.Wait()
		select {
		case err := <-result:
			if err == nil {
				t.Error("Spawn lost cancellation while waiting for a slot")
			}
		default:
			t.Error("Spawn did not finish after cancellation while waiting for a slot")
		}
		done, ok := s.lookupDoneCh("parent/child")
		if !ok {
			t.Error("child was not scheduled")
		} else {
			select {
			case <-done:
			default:
				t.Error("Spawn returned before its slot-waiting child stopped")
			}
		}
		if runCtx.Err() != nil {
			t.Error("child cancellation cancelled the whole run")
		}
		if s.ctx.Err() != nil || s.resolverCtx.Err() != nil {
			t.Error("child cancellation changed the shared dispatcher contexts")
		}
		if childRan {
			t.Error("child ran while the only worker slot was occupied")
		}
		dispatchDone := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(dispatchDone)
		}()
		synctest.Wait()
		select {
		case <-dispatchDone:
		default:
			t.Error("child dispatch remained alive after parent cancellation")
		}
		cancelRun()
		<-dispatchDone
	})
}
