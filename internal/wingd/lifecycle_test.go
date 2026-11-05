package wingd_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type lifecycleIdleProcSampler struct{}

func (lifecycleIdleProcSampler) CPUUsage(int) (wingd.ProcUsage, bool) {
	return wingd.ProcUsage{}, true
}

func holdsRun(qs wingwire.QueueState, runID string) bool {
	for _, h := range qs.Holders {
		if h.RunID == runID {
			return true
		}
	}
	return false
}

func observeHolderFor(t *testing.T, q *client.Client, daemonDone <-chan error, runID string, duration time.Duration) {
	t.Helper()
	deadlineAt := time.Now().Add(duration)
	observe := func() {
		queryCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		qs, err := q.QueueState(queryCtx)
		if err != nil {
			t.Fatalf("queue state while observing holder %q: %v", runID, err)
		}
		if !holdsRun(qs, runID) {
			t.Fatalf("holder %q disappeared during idle observation", runID)
		}
	}
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	observation := time.NewTimer(duration)
	defer observation.Stop()
	for {
		observe()
		if !time.Now().Before(deadlineAt) {
			select {
			case err := <-daemonDone:
				t.Fatalf("daemon exited while holder %q was active: %v", runID, err)
			default:
			}
			return
		}
		select {
		case err := <-daemonDone:
			t.Fatalf("daemon exited while holder %q was active: %v", runID, err)
		case <-poll.C:
		case <-observation.C:
			observe()
			select {
			case err := <-daemonDone:
				t.Fatalf("daemon exited while holder %q was active: %v", runID, err)
			default:
			}
			return
		}
	}
}

func TestVersionSkewPreservesDaemonAndActiveLease(t *testing.T) {
	for _, versions := range []struct{ daemon, client string }{
		{"v1.0.0", "v2.0.0"},
		{"v2.0.0", "v1.0.0"},
		{"v0.22.2", "v0.22.2-dev+1b9e5cd9"},
		{"v1.0.0", "(devel)"},
		{"v1.0.0+dirty", "v1.1.0"},
		{"v0.22.2-dev+e99c1800", "v0.22.2"},
	} {
		t.Run(versions.daemon+"/"+versions.client, func(t *testing.T) {
			home := shortHome(t)
			clock := &stepClock{now: time.Unix(1_700_000_000, 0)}
			td := startDaemon(t, wingd.Config{Home: home, Version: versions.daemon, Now: clock.Now})
			holder := ensure(t, home, versions.daemon)
			lease := mustAcquire(t, holder, coreReq("active", 1))
			other := ensure(t, home, versions.client)
			if other.DaemonVersion() != versions.daemon {
				t.Fatalf("daemon version changed to %q", other.DaemonVersion())
			}
			clock.advance(time.Second)
			state, err := other.QueueState(t.Context())
			if err != nil || !holdsRun(state, lease.RunID) {
				t.Fatalf("active lease changed: state=%+v error=%v", state, err)
			}
			state, err = holder.QueueState(t.Context())
			if err != nil || !holdsRun(state, lease.RunID) {
				t.Fatalf("original holder lost its lease: state=%+v error=%v", state, err)
			}
			if err := lease.Release(); err != nil {
				t.Fatalf("original holder lost its connection: %v", err)
			}

			select {
			case err := <-td.done:
				t.Fatalf("daemon exited: %v", err)
			default:
			}
		})
	}
}

func TestIdleExit_NoWork(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}

	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 250 * time.Millisecond})
	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("idle daemon should exit cleanly, got %v", err)
	}
}

func TestIdleExit_WaitsForHolders(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.8s of real work; the fast class runs under -short")
	}

	home := shortHome(t)
	const idleTimeout = 300 * time.Millisecond
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: idleTimeout})

	a := ensure(t, home, "")
	lease := mustAcquire(t, a, coreReq("a", 1))

	started := time.Now()
	observeHolderFor(t, a, td.done, "a", idleTimeout+100*time.Millisecond)
	elapsed := time.Since(started)
	if elapsed < idleTimeout {
		t.Fatalf("held-lease observation took %s, want at least idle timeout %s", elapsed, idleTimeout)
	}
	if elapsed >= 550*time.Millisecond {
		t.Fatalf("held-lease observation took %s, want under 550ms", elapsed)
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon should idle out after release, got %v", err)
	}
}

func TestQueueState_ReportsHoldersAndWaiters(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30), HeadroomFraction: -1})

	h := ensure(t, home, "")
	mustAcquire(t, h, coreReq("h", 3))

	w := ensure(t, home, "")
	pos, _ := acquireAsync(t, w, coreReq("w", 2))
	waitForQueue(t, pos)

	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}
	if len(qs.Holders) != 1 || qs.Holders[0].RunID != "h" {
		t.Fatalf("holders = %+v, want one holder h", qs.Holders)
	}
	if len(qs.Waiters) != 1 || qs.Waiters[0].RunID != "w" {
		t.Fatalf("waiters = %+v, want one waiter w", qs.Waiters)
	}
	if held := resourceHeld(qs, "cores"); held != 3 {
		t.Fatalf("cores held %v, want 3", held)
	}
}
