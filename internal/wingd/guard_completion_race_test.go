package wingd

import (
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type completionRaceInspector struct {
	reapableGuardInspector
	snapshotEntered   chan struct{}
	snapshotRelease   chan struct{}
	completionEntered chan struct{}
	completionRelease chan struct{}
}

func (inspector *completionRaceInspector) EmptySnapshot() (func(wingwire.ProcessSession) (bool, error), error) {
	close(inspector.snapshotEntered)
	<-inspector.snapshotRelease
	return func(wingwire.ProcessSession) (bool, error) { return true, nil }, nil
}

func (inspector *completionRaceInspector) Empty(wingwire.ProcessSession) (bool, error) {
	close(inspector.completionEntered)
	<-inspector.completionRelease
	return true, nil
}

func TestGuardSweepAcknowledgesCompletionAfterReattach(t *testing.T) {
	inspector := &completionRaceInspector{
		snapshotEntered: make(chan struct{}), snapshotRelease: make(chan struct{}),
		completionEntered: make(chan struct{}), completionRelease: make(chan struct{}),
	}
	releaseSnapshot := sync.OnceFunc(func() { close(inspector.snapshotRelease) })
	releaseCompletion := sync.OnceFunc(func() { close(inspector.completionRelease) })
	t.Cleanup(releaseSnapshot)
	t.Cleanup(releaseCompletion)
	daemon, finalized := guardedHolderDaemon(t, inspector, time.Hour)
	holder, holderPeer := handlerConn(t, daemon)
	grant := mustGrantFrame(t, callAndRead(t, holderPeer, func() {
		daemon.handleAdmission(holder, &wingwire.AdmissionRequest{
			RunID: "returning-worker", SemaphoresOnly: true, Semaphores: exclusiveClaim(), Guard: guardedSession(),
		})
	}))
	daemon.handleDisconnect(holder)
	sweepDone := make(chan error, 1)
	go func() { sweepDone <- daemon.reconcileGuards() }()
	<-inspector.snapshotEntered

	successor, successorPeer := handlerConn(t, daemon)
	mustGrantFrame(t, callAndRead(t, successorPeer, func() {
		daemon.handleReattach(successor, &wingwire.Reattach{LeaseToken: grant.LeaseToken})
	}))
	completionDone := make(chan struct{})
	go func() {
		defer close(completionDone)
		daemon.handleGuardComplete(successor, &wingwire.GuardComplete{LeaseToken: grant.LeaseToken})
	}()
	<-inspector.completionEntered
	acknowledgement := readAsync(successorPeer)
	releaseSnapshot()
	if err := <-sweepDone; err != nil {
		t.Fatalf("reconcile guarded session: %v", err)
	}
	releaseCompletion()
	<-completionDone
	daemon.finalizers.Wait()
	select {
	case runID := <-finalized:
		t.Errorf("reattached run %q was finalized as abandoned", runID)
	default:
	}
	// SAFETY: Both handlers have returned, so closing the writer makes a missing acknowledgement observable.
	successor.close()
	result := <-acknowledgement
	if result.err != nil {
		t.Fatalf("completion acknowledgement: %v", result.err)
	}
	if _, ok := result.msg.(*wingwire.GuardCompleteAck); !ok {
		t.Fatalf("completion reply = %T, want GuardCompleteAck", result.msg)
	}
	daemon.mu.Lock()
	remaining := len(daemon.guards)
	daemon.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("remaining guards = %d, want zero", remaining)
	}
}
