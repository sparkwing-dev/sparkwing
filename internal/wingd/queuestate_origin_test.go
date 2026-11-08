package wingd_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestQueueState_OriginTagsHoldersAndWaiters(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home: home, Version: "v1", HeadroomFraction: -1,
		Sampler: newFakeSampler(8, 16<<30),
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
	positions, result := acquireAsync(t, waiterConn, wingwire.AdmissionRequest{RunID: "ctrl-waiter", Pipeline: "test", Origin: wingwire.OriginController, Resources: wingwire.HostResources{Cores: 6}})

	waitQueued(t, positions, result)
	qs := waiterQueueState(t, home, "ctrl-waiter")

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

func waiterQueueState(t *testing.T, home, runID string) wingwire.QueueState {
	t.Helper()
	qs := queueOnce(t, home)
	for _, waiter := range qs.Waiters {
		if waiter.RunID == runID {
			return qs
		}
	}
	t.Fatalf("waiter %q missing after its queue update: %+v", runID, qs.Waiters)
	return qs
}
