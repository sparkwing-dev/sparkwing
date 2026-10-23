package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestCapacityLevelsKeepIndependentPolicies(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, false, storetest.NewSQLite(t), func(doc *nodeproto.PlanDoc) {
		doc.Concurrency = []nodeproto.ConcurrencySpec{{Group: "shared", Scope: new("global"), Capacity: new(3), OnLimit: new("fail")}}
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "shared", Scope: new("global"), Capacity: new(1), OnLimit: new("queue")}}
	})
	binding := runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}
	result, err := f.tenant.MayStart(t.Context(), binding, f.holder)
	if err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("same name with independent policies: %+v, %v", result, err)
	}
	for key, capacity := range map[string]int{"plan:global:shared": 3, "job:global:shared": 1} {
		state, err := f.tenant.GetConcurrencyState(t.Context(), key)
		if err != nil || len(state.Holders) != 1 || state.EffectiveCapacity != capacity {
			t.Fatalf("independent capacity for %s: %+v, %v", key, state, err)
		}
	}
}
