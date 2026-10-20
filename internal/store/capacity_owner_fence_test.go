package store

import (
	"reflect"
	"testing"
	"time"
)

func FuzzCapacityMutationsRejectReplacedOwnerAndCapturedBinding(f *testing.F) {
	f.Add(uint8(0), uint16(1), false)
	f.Add(uint8(1), uint16(65535), true)
	f.Add(uint8(2), uint16(41), false)
	f.Fuzz(func(t *testing.T, operation uint8, delta uint16, replaceWork bool) {
		tenant, now, actor := capacityFixture(t)
		binding := CapacityBinding{RunID: "run", NodeID: "job", Generation: 3}
		rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','key','holder','run','job',?,?,3)`, now.UnixNano(), now.Add(time.Hour).UnixNano())
		if replaceWork {
			rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET generation=generation+? WHERE team='a' AND run_id='run' AND node_id='job'`, int64(delta)+1)
			current := binding
			current.Generation += int64(delta) + 1
			if _, err := tenant.AdoptCapacity(t.Context(), "key", "holder", current, actor); err != nil {
				t.Fatal(err)
			}
		} else {
			rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET generation=generation+? WHERE name='controller.owner'`, int64(delta)+1)
		}
		before, err := tenant.GetConcurrencyState(t.Context(), "key")
		if err != nil {
			t.Fatal(err)
		}
		switch operation % 3 {
		case 0:
			_, err = tenant.AcquireCapacity(t.Context(), AcquireSlotRequest{Key: "key", HolderID: "holder", RunID: "run", NodeID: "job", Capacity: 1, Cost: 1}, binding, actor)
		case 1:
			_, err = tenant.ResolveCapacityWaiter(t.Context(), "key", binding, actor)
		case 2:
			_, err = tenant.AdoptCapacity(t.Context(), "key", "holder", binding, actor)
		}
		if err == nil {
			t.Fatal("stale authority or binding mutated capacity")
		}
		after, err := tenant.GetConcurrencyState(t.Context(), "key")
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("stale mutation changed successor capacity: %+v -> %+v, %v", before, after, err)
		}
	})
}

func TestCapacityExpiredWaiterCannotBePromotedByRelease(t *testing.T) {
	tenant, now, actor := capacityFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"concurrency":[{"group":"db","scope":"global","capacity":1,"cost":1,"on_limit":"queue","queue_timeout_ms":60000}]}`))
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a','global:db',1,1)`)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','global:db','blocker','run','',?,?,2)`, now.UnixNano(), now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','global:db','waiter','run','job',?,?,3,'queue')`, now.UnixNano(), now.Add(time.Hour).UnixNano())
	*now = now.Add(time.Minute)
	if _, _, promoted, err := tenant.ReleaseAndNotify(t.Context(), "global:db", "blocker", "success", "", "", 0, time.Hour); err != nil || len(promoted) != 0 {
		t.Fatalf("expired waiter promoted on release: %+v, %v", promoted, err)
	}
	got, err := tenant.ResolveCapacityWaiter(t.Context(), "global:db", CapacityBinding{RunID: "run", NodeID: "job", Generation: 3}, actor)
	if err != nil || got.Status != WaiterExpired {
		t.Fatalf("expiry result lost during release: %+v, %v", got, err)
	}
}

func TestCapacityMaintenanceCannotPromoteStaleWaiterBinding(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','key','waiter','run','job',?,?,2,'queue')`, now.UnixNano(), now.Add(time.Hour).UnixNano())
	result, err := tenant.s.AsOperator().MaintainConcurrency(t.Context(), maintenanceReaper(t, tenant.s), ConcurrencyMaintenanceOptions{})
	if err != nil || result.Promoted != 0 {
		t.Fatalf("maintenance rebound a stale waiter to successor work: %+v, %v", result, err)
	}
	state, err := tenant.GetConcurrencyState(t.Context(), "key")
	if err != nil || len(state.Holders) != 0 {
		t.Fatalf("stale waiter became successor holder: %+v, %v", state, err)
	}
}

func TestCapacityQueueExpiryUsesPersistedPolicyAndStoreTime(t *testing.T) {
	tenant, now, actor := capacityFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"concurrency":[{"group":"db","scope":"global","capacity":1,"cost":1,"on_limit":"queue","queue_timeout_ms":60000}]}`))
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a','global:db',1,1)`)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','global:db','blocker','run','',?,?,2)`, now.UnixNano(), now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','global:db','waiter','run','job',?,?,3,'queue')`, now.UnixNano(), now.Add(time.Hour).UnixNano())
	got, err := tenant.ResolveCapacityWaiter(t.Context(), "global:db", CapacityBinding{RunID: "run", NodeID: "job", Generation: 3}, actor)
	if err != nil || got.Status != WaiterStillWaiting {
		t.Fatalf("fresh waiter expired against host clock: %+v, %v", got, err)
	}
	*now = now.Add(time.Minute)
	got, err = tenant.ResolveCapacityWaiter(t.Context(), "global:db", CapacityBinding{RunID: "run", NodeID: "job", Generation: 3}, actor)
	if err != nil || got.Status != WaiterExpired {
		t.Fatalf("store deadline did not expire waiter: %+v, %v", got, err)
	}
	state, err := tenant.GetConcurrencyState(t.Context(), "global:db")
	if err != nil || len(state.Waiters) != 0 || len(state.Holders) != 1 {
		t.Fatalf("expiry changed other capacity: %+v, %v", state, err)
	}
}
