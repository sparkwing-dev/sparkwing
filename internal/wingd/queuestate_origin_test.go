package wingd_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestQueueState_OriginTagsHoldersAndWaiters drives a controller-dispatched
// admission alongside a local one and asserts the daemon carries each run's
// origin onto its queue row, so a shared box's queue attributes contended
// work to whoever launched it.
func TestQueueState_OriginTagsHoldersAndWaiters(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home: home, Version: "v1", GraceWindow: -1,
		HeadroomFraction: -1,
		Sampler:          newFakeSampler(8, 16<<30),
	})

	local := ensure(t, home, "v1")
	mustAcquire(t, local, wingwire.AdmissionRequest{
		RunID:     "local-run",
		Pipeline:  "build",
		Origin:    wingwire.OriginLocal,
		Resources: wingwire.HostResources{Cores: 3},
	})

	ctrl := ensure(t, home, "v1")
	mustAcquire(t, ctrl, wingwire.AdmissionRequest{
		RunID:     "ctrl-run",
		Pipeline:  "deploy",
		Origin:    wingwire.OriginController,
		Resources: wingwire.HostResources{Cores: 1},
	})

	waiterConn := ensure(t, home, "v1")
	go func() {
		_, _ = waiterConn.Acquire(context.Background(), wingwire.AdmissionRequest{
			RunID:     "ctrl-waiter",
			Pipeline:  "test",
			Origin:    wingwire.OriginController,
			Resources: wingwire.HostResources{Cores: 6},
		}, nil)
	}()

	qs := waitForWaiter(t, home, "ctrl-waiter")

	origins := map[string]wingwire.Origin{}
	for _, h := range qs.Holders {
		origins[h.RunID] = h.Origin
	}
	for _, w := range qs.Waiters {
		origins[w.RunID] = w.Origin
	}
	if origins["local-run"] != wingwire.OriginLocal {
		t.Errorf("local-run origin = %q, want local", origins["local-run"])
	}
	if origins["ctrl-run"] != wingwire.OriginController {
		t.Errorf("ctrl-run origin = %q, want controller", origins["ctrl-run"])
	}
	if origins["ctrl-waiter"] != wingwire.OriginController {
		t.Errorf("ctrl-waiter origin = %q, want controller", origins["ctrl-waiter"])
	}
}

// waitForWaiter polls the queue until runID appears as a waiter, so the
// test does not race the queued admission's arrival.
func waitForWaiter(t *testing.T, home, runID string) wingwire.QueueState {
	t.Helper()
	for range 200 {
		qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1"})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		for _, w := range qs.Waiters {
			if w.RunID == runID {
				return qs
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waiter %q never appeared", runID)
	return wingwire.QueueState{}
}
