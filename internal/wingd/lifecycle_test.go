package wingd_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func holdsRun(qs wingwire.QueueState, runID string) bool {
	for _, h := range qs.Holders {
		if h.RunID == runID {
			return true
		}
	}
	return false
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
