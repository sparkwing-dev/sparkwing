package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestCapacityLevelsKeepIndependentPolicies(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, false, storetest.NewSQLite(t), func(doc *nodeproto.PlanDoc) {
		doc.Concurrency = []nodeproto.ConcurrencySpec{{Group: "shared", Scope: new("global"), Capacity: new(3), OnLimit: new("fail")}}
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "shared", Scope: new("global"), Capacity: new(1), OnLimit: new("queue")}}
	})
	binding := store.CapacityBinding{RunID: f.ref.RunID, NodeID: f.ref.NodeID, Generation: f.holder.Generation}
	result, err := f.tenant.AdmitCapacity(t.Context(), binding, f.holder)
	if err != nil || result.Kind != store.AcquireGranted {
		t.Fatalf("same name with independent policies: %+v, %v", result, err)
	}
	for key, capacity := range map[string]int{"plan:global:shared": 3, "job:global:shared": 1} {
		state, err := f.tenant.GetConcurrencyState(t.Context(), key)
		if err != nil || len(state.Holders) != 1 || state.EffectiveCapacity != capacity {
			t.Fatalf("independent capacity for %s: %+v, %v", key, state, err)
		}
	}
}
