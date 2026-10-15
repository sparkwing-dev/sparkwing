package wingd

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestStatsReset_ClearsTheEventWindow(t *testing.T) {
	fixture := &runtimeControllerFixture{}
	runtime := newRuntimeFixture(t, fixture)
	lifecycle := NewLocalLifecycle(runtime, nil)
	if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: fixture.home, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	holder := localControlPipe(t, runtime.run.daemon, false)
	if grant := holder(&wingwire.AdmissionRequest{RunID: "stats-run", Resources: wingwire.HostResources{Cores: 1}}); wingwire.TypeOf(grant) != wingwire.TypeGrant {
		t.Fatalf("admission: %T", grant)
	}
	observe := localControlPipe(t, runtime.run.daemon, false)
	before := observe(&wingwire.QueueState{}).(*wingwire.QueueState)
	if before.Events == nil || before.Events.Runs != 1 || len(before.Holders) != 1 {
		t.Fatalf("grant did not reach event window: %+v", before.Events)
	}
	if reply := observe(&wingwire.StatsReset{}); wingwire.TypeOf(reply) != wingwire.TypeStatsResetAck {
		t.Fatalf("reset stats reply: %T", reply)
	}
	after := observe(&wingwire.QueueState{}).(*wingwire.QueueState)
	if after.Events != nil || len(after.Holders) != 1 || after.Holders[0].RunID != "stats-run" || after.Holders[0].Resources != before.Holders[0].Resources {
		t.Fatalf("reset changed leases or retained events: %+v", after)
	}
	_, events, _, err := readStateWithCancellations(runtime.run.daemon.layout.state)
	if err != nil || len(events) != 0 {
		t.Fatalf("persisted event window: %+v: %v", events, err)
	}
}
