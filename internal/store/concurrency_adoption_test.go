package store

import (
	"context"
	"testing"
	"time"
)

func capacityFixture(t *testing.T) (*Tenant, *time.Time, Actor) {
	t.Helper()
	s, tenant, now := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, RunRunning)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch = 'controller',plan_accepted_generation=1,sealed_at=1,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan' WHERE team = 'a' AND id = 'run'`, []byte(`{}`), []byte(`{"jobs":[{"id":"job"}]}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','owner',?,2)`, now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,generation) VALUES ('a','run','job','pending',?,?,3)`, []byte(`{"id":"job"}`), []byte(`{"concurrency":[{"group":"key","scope":"global","capacity":1,"cost":1,"on_limit":"queue"}]}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a','job:global:key',1,1)`)
	actor := Actor{Kind: ActorController, Owner: OwnerLeaseRef{Kind: OwnerController, Name: "controller.owner", Holder: "owner", Generation: 2}}
	return tenant, now, actor
}

func TestRewriteCapacityAdoptsOnlyContinuousHolders(t *testing.T) {
	tenant, now, actor := capacityFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','job:global:key','holder','run','job',?,?,2)`, now.Add(-time.Minute).UnixNano(), now.Add(time.Minute).UnixNano())
	ctx := context.Background()
	binding := CapacityBinding{RunID: "run", NodeID: "job", Generation: 3}
	adopted, err := tenant.AdoptCapacity(ctx, "job:global:key", "holder", binding, actor)
	if err != nil || adopted != adoptHolder {
		t.Fatalf("adoption: %+v %v", adopted, err)
	}
	var generation int64
	if err := tenant.s.db.QueryRowContext(ctx, `SELECT generation FROM concurrency_holders WHERE team='a' AND key='job:global:key' AND holder_id='holder'`).Scan(&generation); err != nil || generation != 3 {
		t.Fatalf("retained holder generation=%d: %v", generation, err)
	}
	*now = now.Add(time.Minute)
	adopted, err = tenant.AdoptCapacity(ctx, "job:global:key", "holder", binding, actor)
	if err != nil || adopted != acquireAgain {
		t.Fatalf("expiry revived holder: %+v %v", adopted, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "concurrency_holders", 0)
}

func TestRewriteCapacityWaiterKeepsOnlyLiveArrival(t *testing.T) {
	tenant, now, actor := capacityFixture(t)
	arrival := now.Add(-time.Minute)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','job:global:key','waiter','run','job',?,?,2,'queue')`, arrival.UnixNano(), now.Add(time.Minute).UnixNano())
	binding := CapacityBinding{RunID: "run", NodeID: "job", Generation: 3}
	adopted, err := tenant.AdoptCapacity(context.Background(), "job:global:key", "waiter", binding, actor)
	if err != nil || adopted != adoptWaiter {
		t.Fatalf("waiter: %+v %v", adopted, err)
	}
	var retainedArrival int64
	if err := tenant.s.db.QueryRowContext(t.Context(), `SELECT arrived_at FROM concurrency_waiters WHERE team='a' AND key='job:global:key' AND holder_id='waiter'`).Scan(&retainedArrival); err != nil || retainedArrival != arrival.UnixNano() {
		t.Fatalf("retained arrival=%d: %v", retainedArrival, err)
	}
	*now = now.Add(time.Minute)
	adopted, err = tenant.AdoptCapacity(context.Background(), "job:global:key", "waiter", binding, actor)
	if err != nil || adopted != acquireAgain {
		t.Fatalf("expired waiter: %+v %v", adopted, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "concurrency_waiters", 0)
}
