package wingd_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// safety: holding the sweep's first emptiness probe open is the only way to
// place a reattaching client inside the window a daemon restart opens.
type stalledEmptyGuard struct {
	probing  chan struct{}
	release  chan struct{}
	declared chan struct{}
	swept    atomic.Bool
	once     sync.Once
}

func (*stalledEmptyGuard) Validate(wingwire.ProcessSession) error { return nil }

func (*stalledEmptyGuard) Quiescent(wingwire.ProcessSession) (bool, error) { return true, nil }

func (g *stalledEmptyGuard) Empty(wingwire.ProcessSession) (bool, error) {
	if g.swept.CompareAndSwap(false, true) {
		close(g.probing)
		<-g.release
		return true, nil
	}
	g.once.Do(func() { close(g.declared) })
	return false, nil
}

func (*stalledEmptyGuard) Terminate(wingwire.ProcessSession) error { return nil }

// TestGuardCompletionReachesAClientThatReattachedDuringTheSweep drives the
// window a daemon restart opens: the sweep picks up a guard whose client is
// gone, the client comes back and declares completion while the sweep is still
// probing, and the session empties. An acknowledgement addressed to the
// connection the sweep saw loses a run that succeeded.
func TestGuardCompletionReachesAClientThatReattachedDuringTheSweep(t *testing.T) {
	home := shortHome(t)
	guard := &stalledEmptyGuard{
		probing:  make(chan struct{}),
		release:  make(chan struct{}),
		declared: make(chan struct{}),
	}
	finalized := make(chan string, 4)
	startDaemon(t, wingd.Config{
		Home: home, SessionGuardInspector: guard,
		GuardInterval: 10 * time.Millisecond, GraceWindow: time.Minute,
		Runs: &wingd.FuncRunStore{Finalize: func(runID string) { finalized <- runID }},
	})
	var releaseOnce sync.Once
	releaseProbe := func() { releaseOnce.Do(func() { close(guard.release) }) }
	t.Cleanup(releaseProbe)

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID: "reattaching-guard", SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{Name: "exclusive", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue}},
		Guard:      &wingwire.ProcessSession{LeaderPID: 91, SessionID: 91, BirthToken: "birth-91"},
	})
	token := holder.Token
	if err := holderClient.Close(); err != nil {
		t.Fatalf("drop the guarded holder's connection: %v", err)
	}

	select {
	case <-guard.probing:
	case <-time.After(5 * time.Second):
		t.Fatal("the guard sweep never probed the session whose client had gone")
	}

	reattached := ensure(t, home, "")
	lease, err := reattached.Reattach(context.Background(), token)
	if err != nil {
		t.Fatalf("reattach to the guarded lease: %v", err)
	}
	completed := make(chan struct{})
	var completeOnce sync.Once
	go func() { _ = lease.WatchGuard(nil, nil, func() { completeOnce.Do(func() { close(completed) }) }) }()
	if err := lease.CompleteGuard(); err != nil {
		t.Fatalf("declare the guard complete: %v", err)
	}
	select {
	case <-guard.declared:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon never inspected the session for the reattached client's completion")
	}

	releaseProbe()
	select {
	case <-completed:
	case <-time.After(10 * time.Second):
		t.Fatal("the sweep released the guard without acknowledging the client that " +
			"reattached and declared it complete")
	}
	select {
	case runID := <-finalized:
		t.Fatalf("run %s was finalized as abandoned after its client came back", runID)
	default:
	}
}
