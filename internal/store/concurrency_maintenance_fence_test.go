package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

func maintenanceReaper(t *testing.T, s *Store) SingletonLease {
	t.Helper()
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func TestLeasedConcurrencyMaintenancePromotesAfterExpiry(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	s := tenant.s
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','key','expired','run','job',1,?,3)`, now.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','key','next','run','job',1,?,3,'queue')`, now.Add(time.Hour).UnixNano())
	result, err := s.AsOperator().MaintainConcurrency(t.Context(), maintenanceReaper(t, s), ConcurrencyMaintenanceOptions{})
	if err != nil || len(result.StaleHolders) != 1 || result.Promoted != 1 {
		t.Fatalf("maintenance: %+v, %v", result, err)
	}
	state, err := tenant.GetConcurrencyState(t.Context(), "key")
	if err != nil || len(state.Holders) != 1 || state.Holders[0].HolderID != "next" || len(state.Waiters) != 0 || state.UsedCost != 1 {
		t.Fatalf("promoted state: %+v, %v", state, err)
	}
	again, err := s.AsOperator().MaintainConcurrency(t.Context(), maintenanceReaper(t, s), ConcurrencyMaintenanceOptions{})
	if err != nil || !reflect.DeepEqual(again, ConcurrencyMaintenanceResult{}) {
		t.Fatalf("second pass: %+v, %v", again, err)
	}
}

func TestLeasedConcurrencyMaintenanceWaiterContinuity(t *testing.T) {
	for _, scenario := range []string{"live", "expired waiter", "expired owner", "expired claim", "expired owner and claim", "terminal run", "terminal job", "missing job"} {
		t.Run(scenario, func(t *testing.T) {
			tenant, now, _ := capacityFixture(t)
			s := tenant.s
			nodeID := "job"
			until := now.Add(time.Hour)
			switch scenario {
			case "expired waiter":
				until = *now
			case "expired owner":
				rewriteSchemaExec(t, s.db, `UPDATE leases SET until=1 WHERE name='controller.owner'`)
			case "expired claim":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=1 WHERE node_id='job'`)
			case "expired owner and claim":
				rewriteSchemaExec(t, s.db, `UPDATE leases SET until=1 WHERE name='controller.owner'`)
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=1 WHERE node_id='job'`)
			case "terminal run":
				rewriteSchemaExec(t, s.db, `UPDATE runs SET status='success',finished_at=1 WHERE id='run'`)
			case "terminal job":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE node_id='job'`)
			case "missing job":
				nodeID = "missing"
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','key','waiter','run',?,1,?,3,'queue')`, nodeID, until.UnixNano())
			result, err := s.AsOperator().MaintainConcurrency(t.Context(), maintenanceReaper(t, s), ConcurrencyMaintenanceOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "live" || scenario == "expired owner" || scenario == "expired claim" {
				if len(result.StaleWaiters) != 0 || result.Promoted != 1 {
					t.Fatalf("live waiter: %+v", result)
				}
			} else if len(result.StaleWaiters) != 1 || result.Promoted != 0 {
				t.Fatalf("abandoned waiter: %+v", result)
			}
			rewriteSchemaCount(t, s.db, "concurrency_waiters", 0)
		})
	}
}

func TestLeasedConcurrencyMaintenancePreservesBlockedLiveWaiter(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	s := tenant.s
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','other','pending',?,?)`, []byte(`{"id":"other"}`), []byte(`{}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','key','holder','run','job',1,?,3)`, now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','key','waiter','run','other',1,?,0,'queue')`, now.Add(time.Hour).UnixNano())
	before, err := tenant.GetConcurrencyState(t.Context(), "key")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.AsOperator().MaintainConcurrency(t.Context(), maintenanceReaper(t, s), ConcurrencyMaintenanceOptions{})
	if err != nil || !reflect.DeepEqual(result, ConcurrencyMaintenanceResult{}) {
		t.Fatalf("live maintenance: %+v, %v", result, err)
	}
	after, err := tenant.GetConcurrencyState(t.Context(), "key")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("live waiter changed: %+v -> %+v, %v", before, after, err)
	}
}

func TestLeasedConcurrencyMaintenanceRemovesTerminalWorkInEveryScope(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	s := tenant.s
	maintenanceRun(t, s, "finished", 2, RunFailed)
	for _, key := range []string{"run:finished:build", "box:host:build", "global:deploy"} {
		rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a',?,1,1)`, key)
		rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at) VALUES ('a',?,'terminal','finished','',1,?)`, key, now.Add(time.Hour).UnixNano())
		rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,policy) VALUES ('a',?,'terminal','finished','',1,?,'queue')`, key, now.Add(time.Hour).UnixNano())
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','key','live','run','job',1,?,3)`, now.Add(time.Hour).UnixNano())
	result, err := s.AsOperator().MaintainConcurrency(t.Context(), maintenanceReaper(t, s), ConcurrencyMaintenanceOptions{})
	if err != nil || len(result.StaleHolders) != 3 || len(result.StaleWaiters) != 3 || result.Promoted != 0 {
		t.Fatalf("terminal cleanup: %+v, %v", result, err)
	}
	rewriteSchemaCount(t, s.db, "concurrency_holders", 1)
	rewriteSchemaCount(t, s.db, "concurrency_waiters", 0)
	if again, err := s.AsOperator().MaintainConcurrency(t.Context(), maintenanceReaper(t, s), ConcurrencyMaintenanceOptions{}); err != nil || !reflect.DeepEqual(again, ConcurrencyMaintenanceResult{}) {
		t.Fatalf("repeated terminal cleanup: %+v, %v", again, err)
	}
}

func TestConcurrencyMaintenanceFenceProperty(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	s := tenant.s
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','key','expired','run','job',1,?,3)`, now.UnixNano())
	lease := maintenanceReaper(t, s)
	random := rand.New(rand.NewPCG(452, 371))
	for range 96 {
		forged := lease
		switch random.IntN(3) {
		case 0:
			forged.Name = []string{"maintenance", "controller.owner", "' OR 1=1 --", "雪"}[random.IntN(4)]
		case 1:
			forged.Holder += fmt.Sprint(random.Uint64())
		case 2:
			forged.Generation += 1 + random.Int64N(10000)
		}
		result, err := s.AsOperator().MaintainConcurrency(t.Context(), forged, ConcurrencyMaintenanceOptions{})
		if err == nil || !reflect.DeepEqual(result, ConcurrencyMaintenanceResult{}) {
			t.Fatalf("forged fence changed state: %+v, %v", result, err)
		}
		rewriteSchemaCount(t, s.db, "concurrency_holders", 1)
	}
	*now = lease.ExpiresAt
	if _, err := s.AsOperator().MaintainConcurrency(t.Context(), lease, ConcurrencyMaintenanceOptions{}); err == nil {
		t.Fatal("expired reaper swept capacity")
	}
	next, err := s.AsOperator().AcquireSingletonLease(t.Context(), lease.Name, "successor", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().MaintainConcurrency(t.Context(), lease, ConcurrencyMaintenanceOptions{}); err == nil {
		t.Fatal("former reaper swept capacity after takeover")
	}
	if result, err := s.AsOperator().MaintainConcurrency(t.Context(), next, ConcurrencyMaintenanceOptions{}); err != nil || len(result.StaleHolders) != 1 {
		t.Fatalf("successor: %+v, %v", result, err)
	}
}

func TestLeasedConcurrencyMaintenanceRollsBackWholePass(t *testing.T) {
	for _, failure := range []string{"statement", "fence lapse"} {
		t.Run(failure, func(t *testing.T) {
			tenant, now, _ := capacityFixture(t)
			s := tenant.s
			lease := maintenanceReaper(t, s)
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','key','expired','run','job',1,?,3)`, now.UnixNano())
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','key','expired','run','job',1,?,3,'queue')`, now.UnixNano())
			if failure == "statement" {
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_holder_delete BEFORE DELETE ON concurrency_holders BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`)
			} else {
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER expire_reaper AFTER DELETE ON concurrency_holders BEGIN UPDATE leases SET until=1 WHERE name='controller.reaper'; END`)
			}
			result, err := s.AsOperator().MaintainConcurrency(t.Context(), lease, ConcurrencyMaintenanceOptions{})
			if err == nil || !reflect.DeepEqual(result, ConcurrencyMaintenanceResult{}) {
				t.Fatalf("partial result: %+v, %v", result, err)
			}
			rewriteSchemaCount(t, s.db, "concurrency_holders", 1)
			rewriteSchemaCount(t, s.db, "concurrency_waiters", 1)
		})
	}
}
