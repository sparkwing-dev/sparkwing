package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestConcurrencyMaintenanceSweepsWithoutPromoting(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	s := tenant.s
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','next','pending',?,?)`, []byte(`{"id":"next"}`), []byte(`{}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','job:global:key','expired','run','job',1,?,3)`, now.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','job:global:key','next','run','next',1,?,0,'queue')`, now.Add(time.Hour).UnixNano())
	result, err := s.AsOperator().MaintainConcurrency(t.Context())
	if err != nil || len(result.StaleHolders) != 1 {
		t.Fatalf("maintenance: %+v, %v", result, err)
	}
	state, err := tenant.GetConcurrencyState(t.Context(), "job:global:key")
	if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 1 || state.Waiters[0].HolderID != "next" || state.UsedCost != 0 {
		t.Fatalf("waiting state: %+v, %v", state, err)
	}
	again, err := s.AsOperator().MaintainConcurrency(t.Context())
	if err != nil || !reflect.DeepEqual(again, ConcurrencyMaintenanceResult{}) {
		t.Fatalf("second pass: %+v, %v", again, err)
	}
}

func TestConcurrencyMaintenanceWaiterContinuity(t *testing.T) {
	for _, scenario := range []string{"live", "expired waiter", "expired claim", "terminal run", "terminal job", "missing job"} {
		t.Run(scenario, func(t *testing.T) {
			tenant, now, _ := capacityFixture(t)
			s := tenant.s
			nodeID := "job"
			until := now.Add(time.Hour)
			switch scenario {
			case "expired waiter":
				until = *now
			case "expired claim":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=1 WHERE node_id='job'`)
			case "terminal run":
				rewriteSchemaExec(t, s.db, `UPDATE runs SET status='success',finished_at=1 WHERE id='run'`)
			case "terminal job":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE node_id='job'`)
			case "missing job":
				nodeID = "missing"
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','job:global:key','waiter','run',?,?,?,3,'queue')`, nodeID, now.UnixNano(), until.UnixNano())
			result, err := s.AsOperator().MaintainConcurrency(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "live" || scenario == "expired claim" {
				if len(result.StaleWaiters) != 0 {
					t.Fatalf("live waiter: %+v", result)
				}
				rewriteSchemaCount(t, s.db, "concurrency_waiters", 1)
			} else if len(result.StaleWaiters) != 1 {
				t.Fatalf("abandoned waiter: %+v", result)
			} else {
				rewriteSchemaCount(t, s.db, "concurrency_waiters", 0)
			}
			rewriteSchemaCount(t, s.db, "concurrency_holders", 0)
		})
	}
}

func TestConcurrencyMaintenancePreservesBlockedLiveWaiter(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	s := tenant.s
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','other','pending',?,?)`, []byte(`{"id":"other"}`), []byte(`{}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','job:global:key','holder','run','job',1,?,3)`, now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','job:global:key','waiter','run','other',1,?,0,'queue')`, now.Add(time.Hour).UnixNano())
	before, err := tenant.GetConcurrencyState(t.Context(), "job:global:key")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.AsOperator().MaintainConcurrency(t.Context())
	if err != nil || !reflect.DeepEqual(result, ConcurrencyMaintenanceResult{}) {
		t.Fatalf("live maintenance: %+v, %v", result, err)
	}
	after, err := tenant.GetConcurrencyState(t.Context(), "job:global:key")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("live waiter changed: %+v -> %+v, %v", before, after, err)
	}
}

func TestConcurrencyMaintenanceRemovesTerminalWorkInEveryScope(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	s := tenant.s
	maintenanceRun(t, s, "finished", 2, runstate.RunFailed)
	for _, key := range []string{"run:finished:build", "box:host:build", "job:global:deploy"} {
		rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a',?,1,1)`, key)
		rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at) VALUES ('a',?,'terminal','finished','',1,?)`, key, now.Add(time.Hour).UnixNano())
		rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,policy) VALUES ('a',?,'terminal','finished','',1,?,'queue')`, key, now.Add(time.Hour).UnixNano())
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','job:global:key','live','run','job',1,?,3)`, now.Add(time.Hour).UnixNano())
	result, err := s.AsOperator().MaintainConcurrency(t.Context())
	if err != nil || len(result.StaleHolders)+len(result.StaleWaiters) == 0 {
		t.Fatalf("terminal cleanup: %+v, %v", result, err)
	}
	rewriteSchemaCount(t, s.db, "concurrency_holders", 1)
	rewriteSchemaCount(t, s.db, "concurrency_waiters", 0)
	if again, err := s.AsOperator().MaintainConcurrency(t.Context()); err != nil || !reflect.DeepEqual(again, ConcurrencyMaintenanceResult{}) {
		t.Fatalf("repeated terminal cleanup: %+v, %v", again, err)
	}
}

func TestConcurrencyMaintenanceRollsBackWholePass(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	s := tenant.s
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','job:global:key','expired','run','job',1,?,3)`, now.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','job:global:key','expired','run','job',1,?,3,'queue')`, now.UnixNano())
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_holder_delete BEFORE DELETE ON concurrency_holders BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`)
	result, err := s.AsOperator().MaintainConcurrency(t.Context())
	if err == nil || !reflect.DeepEqual(result, ConcurrencyMaintenanceResult{}) {
		t.Fatalf("partial result: %+v, %v", result, err)
	}
	rewriteSchemaCount(t, s.db, "concurrency_holders", 1)
	rewriteSchemaCount(t, s.db, "concurrency_waiters", 1)
}
