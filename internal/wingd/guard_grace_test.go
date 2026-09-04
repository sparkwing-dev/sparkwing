package wingd

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type reapableGuardInspector struct {
	terminated atomic.Bool
	failures   atomic.Int32
}

func (*reapableGuardInspector) Validate(wingwire.ProcessSession) error { return nil }

func (*reapableGuardInspector) Quiescent(wingwire.ProcessSession) (bool, error) { return true, nil }

func (g *reapableGuardInspector) Empty(wingwire.ProcessSession) (bool, error) {
	return g.terminated.Load(), nil
}

func (g *reapableGuardInspector) Terminate(wingwire.ProcessSession) error {
	if g.failures.Add(-1) >= 0 {
		return errTerminateRefused
	}
	g.terminated.Store(true)
	return nil
}

var errTerminateRefused = &terminateError{}

type terminateError struct{}

func (*terminateError) Error() string { return "session refused to die" }

type blockingGuardInspector struct {
	reapableGuardInspector
	entered chan struct{}
	release chan struct{}
}

func newBlockingGuardInspector() *blockingGuardInspector {
	return &blockingGuardInspector{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (g *blockingGuardInspector) Terminate(session wingwire.ProcessSession) error {
	g.entered <- struct{}{}
	<-g.release
	return g.reapableGuardInspector.Terminate(session)
}

func guardedSession() *wingwire.ProcessSession {
	return &wingwire.ProcessSession{LeaderPID: 4242, SessionID: 4242, BirthToken: "birth-4242"}
}

func guardedHolderDaemon(t *testing.T, inspector SessionGuardInspector, grace time.Duration) (*Daemon, chan string) {
	t.Helper()
	finalized := make(chan string, 4)
	d := configuredHandlerDaemon(t, Config{
		SessionGuardInspector: inspector,
		GraceWindow:           grace,
		GuardInterval:         time.Hour,
		Runs:                  &FuncRunStore{Finalize: func(runID string) { finalized <- runID }},
	}, 4)
	return d, finalized
}

func TestDisconnectedGuardedSessionIsReapedWhenItsGraceExpires(t *testing.T) {
	inspector := &reapableGuardInspector{}
	d, finalized := guardedHolderDaemon(t, inspector, 20*time.Millisecond)

	holder, holderPeer := handlerConn(t, d)
	mustGrantFrame(t, callAndRead(t, holderPeer, func() {
		d.handleAdmission(holder, &wingwire.AdmissionRequest{
			RunID: "guarded-run", SemaphoresOnly: true, Semaphores: exclusiveClaim(),
			Guard: guardedSession(),
		})
	}))

	waiter, waiterPeer := handlerConn(t, d)
	callAndRead(t, waiterPeer, func() {
		d.handleAdmission(waiter, &wingwire.AdmissionRequest{
			RunID: "queued-run", SemaphoresOnly: true, Semaphores: exclusiveClaim(),
		})
	})

	promotion := readAsync(waiterPeer)
	d.handleDisconnect(holder)

	grant := mustGrantFrame(t, waitFrame(t, promotion, "promotion grant"))
	if grant.RunID != "queued-run" {
		t.Fatalf("promoted run = %q, want queued-run", grant.RunID)
	}
	if !inspector.terminated.Load() {
		t.Fatal("the abandoned guarded session was released without being terminated")
	}
	select {
	case runID := <-finalized:
		if runID != "guarded-run" {
			t.Fatalf("finalized %q, want guarded-run", runID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the abandoned run was never finalized")
	}
	d.mu.Lock()
	guards := len(d.guards)
	d.mu.Unlock()
	if guards != 0 {
		t.Fatalf("guards still registered = %d, want none; the daemon can never idle out", guards)
	}
}

func TestGuardGraceRetriesAfterATerminationFailure(t *testing.T) {
	inspector := &reapableGuardInspector{}
	inspector.failures.Store(1)
	d, _ := guardedHolderDaemon(t, inspector, 20*time.Millisecond)

	holder, holderPeer := handlerConn(t, d)
	mustGrantFrame(t, callAndRead(t, holderPeer, func() {
		d.handleAdmission(holder, &wingwire.AdmissionRequest{
			RunID: "stubborn-run", SemaphoresOnly: true, Semaphores: exclusiveClaim(),
			Guard: guardedSession(),
		})
	}))

	d.handleDisconnect(holder)

	deadline := time.Now().Add(5 * time.Second)
	for {
		d.mu.Lock()
		guards := len(d.guards)
		d.mu.Unlock()
		if guards == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a failed termination stopped the daemon retrying it")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !inspector.terminated.Load() {
		t.Fatal("the guarded session was released without being terminated")
	}
}

func TestReclaimedGuardedSessionIsNotReaped(t *testing.T) {
	inspector := &reapableGuardInspector{}
	d, _ := guardedHolderDaemon(t, inspector, time.Hour)

	holder, holderPeer := handlerConn(t, d)
	grant := mustGrantFrame(t, callAndRead(t, holderPeer, func() {
		d.handleAdmission(holder, &wingwire.AdmissionRequest{
			RunID: "reclaimed-run", SemaphoresOnly: true, Semaphores: exclusiveClaim(),
			Guard: guardedSession(),
		})
	}))

	d.handleDisconnect(holder)
	if !guardGraceArmed(t, d) {
		t.Fatal("losing a guarded client armed no grace timer")
	}

	successor, successorPeer := handlerConn(t, d)
	reclaimed := mustGrantFrame(t, callAndRead(t, successorPeer, func() {
		d.handleReattach(successor, &wingwire.Reattach{LeaseToken: grant.LeaseToken})
	}))
	if reclaimed.RunID != "reclaimed-run" {
		t.Fatalf("reattached run = %q, want reclaimed-run", reclaimed.RunID)
	}

	if guardGraceArmed(t, d) {
		t.Fatal("a guarded session whose client came back is still counting down to termination")
	}
	if inspector.terminated.Load() {
		t.Fatal("a guarded session whose client came back was terminated anyway")
	}
}

func guardGraceArmed(t *testing.T, d *Daemon) bool {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.guards) != 1 {
		t.Fatalf("guards = %d, want exactly one", len(d.guards))
	}
	for _, guard := range d.guards {
		return guard.graceTimer != nil
	}
	return false
}

func TestReattachIsRefusedWhileTheGuardedSessionIsBeingTerminated(t *testing.T) {
	inspector := newBlockingGuardInspector()
	d, finalized := guardedHolderDaemon(t, inspector, 20*time.Millisecond)

	holder, holderPeer := handlerConn(t, d)
	grant := mustGrantFrame(t, callAndRead(t, holderPeer, func() {
		d.handleAdmission(holder, &wingwire.AdmissionRequest{
			RunID: "terminating-run", SemaphoresOnly: true, Semaphores: exclusiveClaim(),
			Guard: guardedSession(),
		})
	}))

	waiter, waiterPeer := handlerConn(t, d)
	callAndRead(t, waiterPeer, func() {
		d.handleAdmission(waiter, &wingwire.AdmissionRequest{
			RunID: "queued-run", SemaphoresOnly: true, Semaphores: exclusiveClaim(),
		})
	})

	promotion := readAsync(waiterPeer)
	d.handleDisconnect(holder)
	select {
	case <-inspector.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the abandoned guarded session was never terminated")
	}

	successor, successorPeer := handlerConn(t, d)
	reply := callAndRead(t, successorPeer, func() {
		d.handleReattach(successor, &wingwire.Reattach{LeaseToken: grant.LeaseToken})
	})
	evicted, ok := reply.(*wingwire.Evicted)
	if !ok {
		t.Fatalf("reattach during termination = %#v, want an eviction; the client would hold a lease over a dead tree", reply)
	}
	if evicted.Key != "reattach" {
		t.Fatalf("eviction key = %q, want reattach", evicted.Key)
	}

	close(inspector.release)
	grantFrame := mustGrantFrame(t, waitFrame(t, promotion, "promotion grant"))
	if grantFrame.RunID != "queued-run" {
		t.Fatalf("promoted run = %q, want queued-run", grantFrame.RunID)
	}
	select {
	case runID := <-finalized:
		if runID != "terminating-run" {
			t.Fatalf("finalized %q, want terminating-run", runID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the terminated run was never finalized")
	}
}
