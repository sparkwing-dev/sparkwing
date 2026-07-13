package wingd_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

// spawnClient opens a client whose spawn hook brings up a successor daemon
// (restoring durable state) the first time a reconnect finds none running, so
// a mid-operation daemon kill is recovered exactly as production recovers an
// idle-exited or crashed daemon.
func spawnClient(t *testing.T, home string, succ *successor) *client.Client {
	t.Helper()
	cl, err := client.EnsureDaemon(context.Background(), client.Options{
		Home:        home,
		Spawn:       succ.spawn,
		DialTimeout: time.Second,
		Backoff:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// TestChurn_QueuedWaiterRecoversAcrossDaemonKill is the field-down repro: a run
// queued for admission must not surface "use of closed network connection" when
// the daemon blinks. The waiter's Acquire transparently reconnects to the
// respawned daemon, re-submits, and is granted -- no error reaches the run.
func TestChurn_QueuedWaiterRecoversAcrossDaemonKill(t *testing.T) {
	home := shortHome(t)
	td1 := startDaemon(t, wingd.Config{
		Home: home, Sampler: newFakeSampler(1, 8<<30), HeadroomFraction: -1, GraceWindow: 300 * time.Millisecond,
	})

	holder := ensure(t, home, "")
	mustAcquire(t, holder, coreReq("churn-holder", 1))

	succ := newSuccessor(t, home, "")
	waiter := spawnClient(t, home, succ)
	positions, result := acquireAsync(waiter, coreReq("churn-waiter", 1))
	waitForQueue(t, positions)

	td1.stop()
	if err := td1.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon1 exit: %v", err)
	}

	r := waitResult(t, result, wingdChurnWait)
	if r.err != nil {
		t.Fatalf("queued waiter surfaced an error across the daemon kill: %v", r.err)
	}
	if r.lease == nil || r.lease.RunID != "churn-waiter" {
		t.Fatalf("waiter recovered lease = %+v, want a grant for churn-waiter", r.lease)
	}
}

// TestChurn_HolderWatchReattachesAcrossKill proves a holder's lease survives a
// daemon blink: after the daemon is killed and respawned, the holder's watcher
// reconnects and reattaches within the grace window. Survival past the grace
// window is the proof -- an unclaimed orphan would have been released -- and
// the recovered connection can still cleanly release the lease afterward.
func TestChurn_HolderWatchReattachesAcrossKill(t *testing.T) {
	home := shortHome(t)
	td1 := startDaemon(t, wingd.Config{Home: home, GraceWindow: 300 * time.Millisecond})

	succ := newSuccessor(t, home, "")
	holderCl := spawnClient(t, home, succ)
	lease, err := holderCl.Acquire(context.Background(), coreReq("churn-watch", 1), nil)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	go lease.WatchControl(nil, nil)

	td1.stop()
	if err := td1.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon1 exit: %v", err)
	}

	select {
	case <-succ.ready:
	case <-time.After(wingdChurnWait):
		t.Fatal("successor daemon never came up")
	}
	time.Sleep(successorGrace + 500*time.Millisecond)
	waitForHolder(t, home, "churn-watch")

	if err := lease.Release(); err != nil {
		t.Fatalf("release after reattach: %v", err)
	}
}

// TestChurn_QueueStateRecoversAcrossKill checks the read-only status path: a
// persistent client's QueueState survives a daemon kill by reconnecting to the
// respawned daemon rather than failing the read.
func TestChurn_QueueStateRecoversAcrossKill(t *testing.T) {
	home := shortHome(t)
	td1 := startDaemon(t, wingd.Config{Home: home, GraceWindow: 300 * time.Millisecond})

	succ := newSuccessor(t, home, "")
	cl := spawnClient(t, home, succ)
	if _, err := cl.QueueState(context.Background()); err != nil {
		t.Fatalf("initial queue state: %v", err)
	}

	td1.stop()
	if err := td1.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon1 exit: %v", err)
	}

	qs, err := cl.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state did not recover across the daemon kill: %v", err)
	}
	_ = qs
}

const wingdChurnWait = 10 * time.Second

// successorGrace mirrors the reattach grace window the successor daemon in
// newSuccessor is configured with, so a test can wait out an unclaimed orphan.
const successorGrace = 2 * time.Second

func waitForHolder(t *testing.T, home, runID string) {
	t.Helper()
	deadline := time.Now().Add(wingdChurnWait)
	for time.Now().Before(deadline) {
		q := ensure(t, home, "")
		qs, err := q.QueueState(context.Background())
		_ = q.Close()
		if err == nil {
			for _, h := range qs.Holders {
				if h.RunID == runID {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %q never reappeared as a holder after reattach", runID)
}
