package wingd_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type controlledSessionGuard struct {
	empty atomic.Bool
}

func (*controlledSessionGuard) Validate(wingwire.ProcessSession) error { return nil }

func (g *controlledSessionGuard) Quiescent(wingwire.ProcessSession) (bool, error) {
	return g.empty.Load(), nil
}

func (g *controlledSessionGuard) Empty(wingwire.ProcessSession) (bool, error) {
	return g.empty.Load(), nil
}

func (g *controlledSessionGuard) Terminate(wingwire.ProcessSession) error {
	g.empty.Store(true)
	return nil
}

func TestGuardedCancellationRetainsAdmissionUntilSessionStops(t *testing.T) {
	home := shortHome(t)
	guard := &controlledSessionGuard{}
	guard.empty.Store(true)
	startDaemon(t, wingd.Config{
		Home: home, SessionGuardInspector: guard, GuardInterval: 10 * time.Millisecond,
	})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID: "guarded-holder", SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{Name: "exclusive", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue}},
		Guard:      &wingwire.ProcessSession{LeaderPID: 41, SessionID: 41, BirthToken: "birth-41"},
	})
	guard.empty.Store(false)

	followerClient := ensure(t, home, "")
	_, follower := acquireAsync(followerClient, wingwire.AdmissionRequest{
		RunID: "guarded-follower", SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{Name: "exclusive", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue}},
	})

	cancelSeen := make(chan struct{})
	finishSession := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		holder.WatchGuard(nil, func(wingwire.Cancel) {
			close(cancelSeen)
			<-finishSession
			guard.empty.Store(true)
			_ = holder.CompleteGuard()
		}, nil)
		close(watchDone)
	}()

	control := ensure(t, home, "")
	found, err := control.CancelLease(context.Background(), "guarded-holder")
	if err != nil || !found {
		t.Fatalf("cancel guarded holder: found=%v err=%v", found, err)
	}
	select {
	case <-cancelSeen:
	case <-time.After(time.Second):
		t.Fatal("guarded holder did not receive cancellation")
	}
	select {
	case result := <-follower:
		if result.lease != nil {
			_ = result.lease.Release()
		}
		t.Fatalf("follower promoted before guarded session stopped: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}

	close(finishSession)
	result := waitResult(t, follower, 2*time.Second)
	if result.err != nil {
		t.Fatalf("follower after guarded session stopped: %v", result.err)
	}
	if err := result.lease.Release(); err != nil {
		t.Fatalf("release follower: %v", err)
	}
	select {
	case <-watchDone:
	case <-time.After(time.Second):
		t.Fatal("guarded completion was not acknowledged")
	}
}
