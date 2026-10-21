package store

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func capacityFixture(t *testing.T) (*Tenant, *time.Time, Actor) {
	t.Helper()
	s, tenant, now := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, RunRunning)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch = 'controller',plan_accepted_generation=1 WHERE team = 'a' AND id = 'run'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','owner',?,2)`, now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,generation) VALUES ('a','run','job','pending',?,?,3)`, []byte(`{"id":"job"}`), []byte(`{}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a','key',1,1)`)
	actor := Actor{Kind: ActorController, Owner: OwnerLeaseRef{Kind: OwnerController, Name: "controller.owner", EvalFence: runspec.EvalFence{Holder: "owner", Generation: 2}}}
	return tenant, now, actor
}

func TestRewriteCapacityAdoptsOnlyContinuousHolders(t *testing.T) {
	tenant, now, actor := capacityFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','key','holder','run','job',?,?,2)`, now.Add(-time.Minute).UnixNano(), now.Add(time.Minute).UnixNano())
	ctx := context.Background()
	binding := CapacityBinding{RunID: "run", NodeID: "job", Generation: 3}
	adopted, err := tenant.AdoptCapacity(ctx, "key", "holder", binding, actor)
	if err != nil || adopted.Kind != AdoptHolder || adopted.Holder.Binding.Generation != 3 {
		t.Fatalf("adoption: %+v %v", adopted, err)
	}
	*now = now.Add(time.Minute)
	adopted, err = tenant.AdoptCapacity(ctx, "key", "holder", binding, actor)
	if err != nil || adopted.Kind != AcquireAgain {
		t.Fatalf("expiry revived holder: %+v %v", adopted, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "concurrency_holders", 0)
}

func TestRewriteCapacityWaiterKeepsOnlyLiveArrival(t *testing.T) {
	tenant, now, actor := capacityFixture(t)
	arrival := now.Add(-time.Minute)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','key','waiter','run','job',?,?,2,'queue')`, arrival.UnixNano(), now.Add(time.Minute).UnixNano())
	binding := CapacityBinding{RunID: "run", NodeID: "job", Generation: 3}
	adopted, err := tenant.AdoptCapacity(context.Background(), "key", "waiter", binding, actor)
	if err != nil || adopted.Kind != AdoptWaiter || !adopted.Waiter.ArrivedAt.Equal(arrival) {
		t.Fatalf("waiter: %+v %v", adopted, err)
	}
	*now = now.Add(time.Minute)
	adopted, err = tenant.AdoptCapacity(context.Background(), "key", "waiter", binding, actor)
	if err != nil || adopted.Kind != AcquireAgain {
		t.Fatalf("expired waiter: %+v %v", adopted, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "concurrency_waiters", 0)
}
