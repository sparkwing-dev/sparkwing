package nodehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type cancelledChildClaimStore struct {
	*acquiredClaimStore
	mu        sync.Mutex
	cancelled atomic.Bool
	at        time.Time
}

func (s *cancelledChildClaimStore) ReadSnapshot(ctx context.Context, run string) (store.RunSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.acquiredClaimStore.ReadSnapshot(ctx, run)
	if s.cancelled.Load() {
		snapshot.Run.CancelRequestedAt = &s.at
	}
	return snapshot, err
}

func (s *cancelledChildClaimStore) ReadNode(ctx context.Context, ref runspec.NodeRef) (runstate.NodeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquiredClaimStore.ReadNode(ctx, ref)
}

func (s *cancelledChildClaimStore) TransitionNode(ctx context.Context, command store.NodeCommand) (runstate.Applied, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquiredClaimStore.TransitionNode(ctx, command)
}

func (s *cancelledChildClaimStore) RenewNodeClaim(ctx context.Context, ref runspec.NodeRef, actor runstate.Actor) (runstate.LeaseBeat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquiredClaimStore.RenewNodeClaim(ctx, ref, actor)
}

func TestClaimedChildHostObservesCancellationBeforeLeaseRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		input, state, host, processes := claimedNodeFixture(t, func(_ *NodeInput, deps *ExecutorDependencies) {
			start := deps.Start
			deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, input, activity)
				if err == nil {
					process.(*executorProcess).onRun = func(ctx context.Context) {
						close(started)
						<-ctx.Done()
					}
				}
				return process, err
			}
		})
		host.Lease = lease.Keeper{Lease: limits.DefaultLease, Interval: limits.Renew(limits.DefaultLease)}
		state.run.Lineage.ParentRunID = "parent"
		cancelled := &cancelledChildClaimStore{acquiredClaimStore: state, at: time.Now()}
		host.Store = cancelled
		host.Executor.(*nodeExecutor).deps.Store = cancelled
		beats := 0
		state.renew = func(context.Context) (runstate.LeaseBeat, error) {
			beats++
			return runstate.LeaseBeat{CancelRequested: cancelled.cancelled.Load()}, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		type exit struct {
			err error
			at  time.Time
		}
		done := make(chan exit, 1)
		go func() { err := host.Execute(ctx, input); done <- exit{err, time.Now()} }()
		<-started
		// safety: The persisted child cancel isolates host observation from controller cascade timing.
		requestedAt := time.Now()
		cancelled.cancelled.Store(true)
		synctest.Wait()
		time.Sleep(12 * time.Second)
		select {
		case stopped := <-done:
			var requested *lease.CancelError
			if !errors.As(stopped.err, &requested) {
				t.Fatalf("child host lost cancellation cause: %v", stopped.err)
			}
			if elapsed := stopped.at.Sub(requestedAt); elapsed >= 12*time.Second {
				t.Fatalf("child host took %s to stop after cancellation", elapsed)
			}
		default:
			t.Error("awaited child host remained alive at the 12-second cancellation bound")
			cancel()
			<-done
		}
		if beats != 1 {
			t.Fatalf("cancellation changed the three-minute lease renewal cadence: %d renewals", beats)
		}
		if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
			t.Fatal("child host left its execution process open")
		}
	})
}
