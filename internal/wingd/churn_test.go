package wingd_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func spawnClient(t *testing.T, home string, succ *successor) *client.Client {
	t.Helper()
	cl, err := client.EnsureDaemon(context.Background(), client.Options{
		Home:        home,
		Version:     "v1.0.0",
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

func TestChurn_HolderWatchReattachesAcrossKill(t *testing.T) {
	t.Parallel()

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
	waitForHolder(t, home, "churn-watch")
	observeReattachedHolderFor(t, home, "churn-watch", successorGrace+500*time.Millisecond)

	if err := lease.Release(); err != nil {
		t.Fatalf("release after reattach: %v", err)
	}
}

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

const successorGrace = 2 * time.Second

func waitForHolder(t *testing.T, home, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wingdChurnWait)
	defer cancel()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	var lastErr error
	for {
		q := ensure(t, home, "")
		qs, err := q.QueueState(ctx)
		_ = q.Close()
		if err == nil {
			for _, h := range qs.Holders {
				if h.RunID == runID {
					return
				}
			}
		} else {
			lastErr = err
		}
		select {
		case <-poll.C:
		case <-ctx.Done():
			if lastErr != nil {
				t.Fatalf("run %q never reappeared as a holder after reattach; last queue error: %v", runID, lastErr)
			}
			t.Fatalf("run %q never reappeared as a holder after reattach", runID)
		}
	}
}

func observeReattachedHolderFor(t *testing.T, home, runID string, duration time.Duration) {
	t.Helper()
	q := ensure(t, home, "")
	defer func() { _ = q.Close() }()
	started := time.Now()
	deadlineAt := started.Add(duration)
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	observation := time.NewTimer(duration)
	defer observation.Stop()
	for {
		queryCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		qs, err := q.QueueState(queryCtx)
		cancel()
		if err != nil {
			t.Fatalf("queue state while observing reattached holder %q: %v", runID, err)
		}
		if !holdsRun(qs, runID) {
			t.Fatalf("reattached holder %q disappeared during its successor grace observation", runID)
		}
		if !time.Now().Before(deadlineAt) {
			if elapsed := time.Since(started); elapsed < duration {
				t.Fatalf("reattached holder observation took %s, want at least %s", elapsed, duration)
			}
			return
		}
		select {
		case <-poll.C:
		case <-observation.C:
		}
	}
}
