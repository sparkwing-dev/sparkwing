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
	empty      atomic.Bool
	terminated atomic.Bool
}

func (*controlledSessionGuard) Validate(wingwire.ProcessSession) error { return nil }

func (g *controlledSessionGuard) Quiescent(wingwire.ProcessSession) (bool, error) {
	return g.empty.Load(), nil
}

func (g *controlledSessionGuard) Empty(wingwire.ProcessSession) (bool, error) {
	return g.empty.Load(), nil
}

func (g *controlledSessionGuard) Terminate(wingwire.ProcessSession) error {
	g.terminated.Store(true)
	g.empty.Store(true)
	return nil
}

type distinctSessionGuard struct {
	empty atomic.Bool
}

func (*distinctSessionGuard) Validate(wingwire.ProcessSession) error { return nil }

func (*distinctSessionGuard) Quiescent(wingwire.ProcessSession) (bool, error) { return true, nil }

func (g *distinctSessionGuard) Empty(wingwire.ProcessSession) (bool, error) {
	return g.empty.Load(), nil
}

func (g *distinctSessionGuard) Terminate(wingwire.ProcessSession) error {
	g.empty.Store(true)
	return nil
}

func TestGuardCompletionRequiresAnEmptySession(t *testing.T) {
	home := shortHome(t)
	guard := &distinctSessionGuard{}
	startDaemon(t, wingd.Config{
		Home: home, SessionGuardInspector: guard, GuardInterval: 10 * time.Millisecond,
	})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID: "nonempty-guard", SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{Name: "exclusive", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue}},
		Guard:      &wingwire.ProcessSession{LeaderPID: 37, SessionID: 37, BirthToken: "birth-37"},
	})
	followerClient := ensure(t, home, "")
	_, follower := acquireAsync(followerClient, wingwire.AdmissionRequest{
		RunID: "nonempty-follower", SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{Name: "exclusive", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue}},
	})

	completed := make(chan struct{})
	go holder.WatchGuard(nil, nil, func() { close(completed) })
	if err := holder.CompleteGuard(); err != nil {
		t.Fatalf("declare nonempty completion: %v", err)
	}
	select {
	case result := <-follower:
		if result.lease != nil {
			_ = result.lease.Release()
		}
		t.Fatalf("follower promoted while guarded session was nonempty: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-completed:
		t.Fatal("daemon acknowledged completion while guarded session was nonempty")
	default:
	}

	guard.empty.Store(true)
	if err := holder.CompleteGuard(); err != nil {
		t.Fatalf("complete empty guard: %v", err)
	}
	result := waitResult(t, follower, 2*time.Second)
	if result.err != nil {
		t.Fatalf("follower after empty guard: %v", result.err)
	}
	_ = result.lease.Release()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("empty guarded completion was not acknowledged")
	}
}

func TestDisconnectedGuardedSessionRemainsCancellable(t *testing.T) {
	home := shortHome(t)
	guard := &controlledSessionGuard{}
	guard.empty.Store(true)
	startDaemon(t, wingd.Config{
		Home: home, SessionGuardInspector: guard, GuardInterval: 10 * time.Millisecond,
	})

	holderClient := ensure(t, home, "")
	mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID: "disconnected-guard", SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{Name: "exclusive", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue}},
		Guard:      &wingwire.ProcessSession{LeaderPID: 63, SessionID: 63, BirthToken: "birth-63"},
	})
	guard.empty.Store(false)
	if err := holderClient.Close(); err != nil {
		t.Fatalf("disconnect guarded supervisor: %v", err)
	}

	followerClient := ensure(t, home, "")
	_, follower := acquireAsync(followerClient, wingwire.AdmissionRequest{
		RunID: "disconnected-follower", SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{Name: "exclusive", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue}},
	})
	control := ensure(t, home, "")
	found, err := control.CancelLease(context.Background(), "disconnected-guard")
	if err != nil || !found {
		t.Fatalf("cancel disconnected guarded session: found=%v err=%v", found, err)
	}
	if !guard.terminated.Load() {
		t.Fatal("daemon did not terminate the disconnected guarded session")
	}
	result := waitResult(t, follower, 2*time.Second)
	if result.err != nil {
		t.Fatalf("follower after disconnected cancellation: %v", result.err)
	}
	_ = result.lease.Release()
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

func TestGuardedLeaseReattachesAfterDaemonRestart(t *testing.T) {
	home := shortHome(t)
	guard := &controlledSessionGuard{}
	guard.empty.Store(true)
	first := startDaemon(t, wingd.Config{
		Home: home, SessionGuardInspector: guard, GuardInterval: 10 * time.Millisecond,
	})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID: "guarded-restart", Resources: wingwire.HostResources{Cores: 1},
		Guard: &wingwire.ProcessSession{LeaderPID: 52, SessionID: 52, BirthToken: "birth-52"},
	})
	guard.empty.Store(false)
	first.stop()
	if err := first.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("stop first daemon: %v", err)
	}

	startDaemon(t, wingd.Config{
		Home: home, SessionGuardInspector: guard, GuardInterval: 10 * time.Millisecond,
	})
	reconnector := ensure(t, home, "")
	reclaimed, err := reconnector.Reattach(context.Background(), holder.Token)
	if err != nil {
		t.Fatalf("reattach guarded lease: %v", err)
	}
	if reclaimed.RunID != "guarded-restart" || reclaimed.Token != holder.Token {
		t.Fatalf("reattached guarded lease = %+v", reclaimed)
	}
	guard.empty.Store(true)
	if err := reclaimed.CompleteGuard(); err != nil {
		t.Fatalf("complete guarded lease: %v", err)
	}
}
