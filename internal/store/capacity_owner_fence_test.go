package store

import (
	"testing"
	"time"
)

func TestCapacityMaintenanceCannotPromotePartOfAdmissionSet(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO admission_work(team,run_id,node_id,arrived_at,lease_expires_at,generation,holder) VALUES ('a','run','job',?,?,2,'waiter')`, now.UnixNano(), now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','job:global:key','waiter','run','job',?,?,2,'queue')`, now.UnixNano(), now.Add(time.Hour).UnixNano())
	result, err := tenant.s.AsOperator().MaintainConcurrency(t.Context(), maintenanceReaper(t, tenant.s))
	if err != nil || len(result.StaleHolders) != 0 || len(result.StaleWaiters) != 0 {
		t.Fatalf("maintenance rebound a stale waiter to successor work: %+v, %v", result, err)
	}
	state, err := tenant.GetConcurrencyState(t.Context(), "job:global:key")
	if err != nil || len(state.Holders) != 0 {
		t.Fatalf("stale waiter became successor holder: %+v, %v", state, err)
	}
}
