package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

func TestConcurrencyMutationsRemainTenantScopedProperty(t *testing.T) {
	s, a, _ := maintenanceStore(t)
	b := &Tenant{s: s, team: "b"}
	for i, team := range []Team{"a", "b"} {
		maintenanceRun(t, s, string(team), i+1, RunRunning)
		rewriteSchemaExec(t, s.db, `UPDATE runs SET team=? WHERE id=?`, team, team)
		rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES (?, ?, 'job', 'pending', ?, ?)`, team, team, []byte(`{"id":"job"}`), []byte(`{}`))
	}
	random := rand.New(rand.NewPCG(652, 972))
	for range 96 {
		key := fmt.Sprintf("%s/%x", []string{"deploy", "' OR 1=1 --", "_%", "a/b", "雪"}[random.IntN(5)], random.Uint64())
		holder := []string{"shared", "' OR 1=1 --", "_%", "雪"}[random.IntN(4)]
		request := AcquireSlotRequest{Key: key, HolderID: holder, NodeID: "job", Capacity: 1, Cost: 1, Lease: time.Hour, Policy: OnLimitQueue}
		for _, tenant := range []*Tenant{a, b} {
			request.RunID = string(tenant.team)
			got, err := tenant.AcquireConcurrencySlot(t.Context(), request)
			if err != nil || got.Kind != AcquireGranted {
				t.Fatalf("%s acquire: %+v, %v", tenant.team, got, err)
			}
		}
		before, err := b.GetConcurrencyState(t.Context(), key)
		if err != nil || len(before.Holders) != 1 || before.UsedCost != 1 {
			t.Fatalf("foreign state: %+v, %v", before, err)
		}
		check := func() {
			t.Helper()
			after, err := b.GetConcurrencyState(t.Context(), key)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("mutation crossed tenant: %+v -> %+v, %v", before, after, err)
			}
		}
		if _, superseded, err := a.HeartbeatConcurrencySlot(t.Context(), key, holder, time.Hour); err != nil || superseded {
			t.Fatalf("heartbeat: superseded=%v, %v", superseded, err)
		}
		check()
		if removed, err := a.CancelWaiter(t.Context(), key, "b", "job"); err != nil || removed {
			t.Fatalf("foreign waiter: removed=%v, %v", removed, err)
		}
		check()
		if released, _, _, err := a.ReleaseAndNotify(t.Context(), key, holder, "success", "", "", 0, time.Hour); err != nil || !released {
			t.Fatalf("release: released=%v, %v", released, err)
		}
		check()
		if _, err := a.PromoteNextWaiters(t.Context(), key, time.Hour); err != nil {
			t.Fatal(err)
		}
		check()
		if _, err := a.ResolveWaiter(t.Context(), key, "b", "job", "", "", "", false); err != nil {
			t.Fatal(err)
		}
		check()
		if _, err := a.ForceReleaseSupersededHolders(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		check()
	}
	states, err := b.ListConcurrencyStates(t.Context())
	if err != nil || len(states) != 96 {
		t.Fatalf("foreign listing=%d, %v", len(states), err)
	}
}

func TestConcurrencyExpiredCapacityNeverRevivesProperty(t *testing.T) {
	tenant, now, actor := capacityFixture(t)
	random := rand.New(rand.NewPCG(194, 672))
	for range 96 {
		lease := time.Duration(1 + random.Int64N(int64(time.Second)))
		request := AcquireSlotRequest{Key: "global:key", HolderID: "holder", RunID: "run", NodeID: "job", Capacity: 1, Cost: 1, Lease: lease, Policy: OnLimitQueue}
		got, err := tenant.AcquireConcurrencySlot(t.Context(), request)
		if err != nil || got.Kind != AcquireGranted || !got.LeaseExpiresAt.Equal(now.Add(lease)) {
			t.Fatalf("acquire: %+v, %v", got, err)
		}
		*now = got.LeaseExpiresAt.Add(time.Duration(random.Int64N(2)))
		if _, _, err := tenant.HeartbeatConcurrencySlot(t.Context(), "global:key", "holder", time.Hour); err == nil {
			t.Fatal("heartbeat revived expired capacity")
		}
		adopted, err := tenant.AdoptCapacity(t.Context(), "global:key", "holder", CapacityBinding{RunID: "run", NodeID: "job", Generation: 3}, actor)
		if err != nil || adopted != acquireAgain {
			t.Fatalf("expired capacity adopted: %+v, %v", adopted, err)
		}
		state, err := tenant.GetConcurrencyState(t.Context(), "global:key")
		if err != nil || state.UsedCost != 0 || len(state.Holders) != 0 {
			t.Fatalf("expired capacity counted: %+v, %v", state, err)
		}
	}
}
