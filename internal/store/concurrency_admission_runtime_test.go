package store

import (
	"errors"
	"testing"
	"time"
)

func TestHolderLivenessUsesStoreClock(t *testing.T) {
	tenant, now, actor := capacityFixture(t)
	request := AcquireSlotRequest{Key: "key", HolderID: "holder", RunID: "run", NodeID: "job", Capacity: 1, Cost: 1, Lease: time.Minute, Policy: OnLimitQueue}
	got, err := tenant.AcquireConcurrencySlot(t.Context(), request)
	if err != nil || got.Kind != AcquireGranted || !got.LeaseExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("clocked acquisition: %+v, %v", got, err)
	}
	*now = got.LeaseExpiresAt
	state, err := tenant.GetConcurrencyState(t.Context(), "key")
	if err != nil || state.UsedCost != 0 {
		t.Fatalf("expired capacity counted: %+v, %v", state, err)
	}
	if _, _, err := tenant.HeartbeatConcurrencySlot(t.Context(), "key", "holder", time.Minute); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("heartbeat revived expired capacity: %v", err)
	}
	adopted, err := tenant.AdoptCapacity(t.Context(), "key", "holder", CapacityBinding{RunID: "run", NodeID: "job", Generation: 3}, actor)
	if err != nil || adopted.Kind != AcquireAgain {
		t.Fatalf("adoption revived expired capacity: %+v, %v", adopted, err)
	}
	got, err = tenant.AcquireConcurrencySlot(t.Context(), request)
	if err != nil || got.Kind != AcquireGranted || !got.LeaseExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("new arrival: %+v, %v", got, err)
	}
}

func TestConcurrencyAdmissionPersistsWorkAndWaiterFences(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	maintenanceRun(t, tenant.s, "waiter", 2, RunRunning)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET dispatch='controller',plan_accepted_generation=1 WHERE id='waiter'`)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,generation) VALUES ('a','waiter','job','pending',?,?,7)`, []byte(`{"id":"job"}`), []byte(`{}`))
	request := AcquireSlotRequest{Key: "key", HolderID: "leader", RunID: "run", NodeID: "job", Capacity: 1, Cost: 1, Lease: time.Hour, Policy: OnLimitQueue}
	if got, err := tenant.AcquireConcurrencySlot(t.Context(), request); err != nil || got.Kind != AcquireGranted {
		t.Fatalf("leader: %+v, %v", got, err)
	}
	var generation int64
	if err := tenant.s.db.QueryRowContext(t.Context(), `SELECT generation FROM concurrency_holders WHERE team='a' AND key='key' AND holder_id='leader'`).Scan(&generation); err != nil || generation != 3 {
		t.Fatalf("holder generation=%d, %v", generation, err)
	}
	request.HolderID, request.RunID = "follower", "waiter"
	if got, err := tenant.AcquireConcurrencySlot(t.Context(), request); err != nil || got.Kind != AcquireQueued {
		t.Fatalf("follower: %+v, %v", got, err)
	}
	var expiry int64
	if err := tenant.s.db.QueryRowContext(t.Context(), `SELECT generation,lease_expires_at FROM concurrency_waiters WHERE team='a' AND key='key' AND run_id='waiter'`).Scan(&generation, &expiry); err != nil || generation != 7 || expiry <= now.UnixNano() {
		t.Fatalf("waiter fence=(%d,%d), %v", generation, expiry, err)
	}
	if released, _, promoted, err := tenant.ReleaseAndNotify(t.Context(), "key", "leader", "success", "", "", 0, time.Hour); err != nil || !released || len(promoted) != 1 || promoted[0].HolderID != "follower" {
		t.Fatalf("promotion: released=%v, promoted=%+v, %v", released, promoted, err)
	}
	if err := tenant.s.db.QueryRowContext(t.Context(), `SELECT generation FROM concurrency_holders WHERE team='a' AND key='key' AND holder_id='follower'`).Scan(&generation); err != nil || generation != 7 {
		t.Fatalf("promoted generation=%d, %v", generation, err)
	}
}

func TestConcurrencyQueuedPromotionUsesOwnerLease(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(map[bool]string{true: "live", false: "expired"}[live], func(t *testing.T) {
			tenant, now, _ := capacityFixture(t)
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','key','waiter','run','job',?,?,3,'queue')`, now.Add(-time.Minute).UnixNano(), now.Add(time.Hour).UnixNano())
			if !live {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET until=1 WHERE name='controller.owner'`)
			}
			promoted, err := tenant.PromoteNextWaiters(t.Context(), "key", time.Hour)
			if err != nil || len(promoted) != map[bool]int{true: 1, false: 0}[live] {
				t.Fatalf("promotion=%+v, %v", promoted, err)
			}
		})
	}
}

func TestConcurrencyExpiredWaiterBecomesANewArrival(t *testing.T) {
	tenant, now, _ := capacityFixture(t)
	for _, id := range []string{"returning", "earlier"} {
		rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run',?,'pending',?,?)`, id, []byte(`{}`), []byte(`{}`))
	}
	request := AcquireSlotRequest{Key: "key", HolderID: "leader", RunID: "run", NodeID: "job", Capacity: 1, Cost: 1, Lease: time.Hour, Policy: OnLimitQueue}
	if _, err := tenant.AcquireConcurrencySlot(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.HolderID, request.NodeID, request.Lease = "returning", "returning", time.Minute
	if got, err := tenant.AcquireConcurrencySlot(t.Context(), request); err != nil || got.Kind != AcquireQueued {
		t.Fatalf("returning waiter: %+v, %v", got, err)
	}
	request.HolderID, request.NodeID, request.Lease = "earlier", "earlier", time.Hour
	if got, err := tenant.AcquireConcurrencySlot(t.Context(), request); err != nil || got.Kind != AcquireQueued {
		t.Fatalf("earlier waiter: %+v, %v", got, err)
	}
	*now = now.Add(2 * time.Minute)
	request.HolderID, request.NodeID = "returning", "returning"
	if got, err := tenant.AcquireConcurrencySlot(t.Context(), request); err != nil || got.Kind != AcquireQueued || got.Position != 1 {
		t.Fatalf("expired waiter retained its place: %+v, %v", got, err)
	}
	if released, _, promoted, err := tenant.ReleaseAndNotify(t.Context(), "key", "leader", "success", "", "", 0, time.Hour); err != nil || !released || len(promoted) != 1 || promoted[0].HolderID != "earlier" {
		t.Fatalf("expired waiter bypassed earlier arrival: released=%v, %+v, %v", released, promoted, err)
	}
}

func TestConcurrencyUsesTimeAfterCapacityLocks(t *testing.T) {
	for _, maintenance := range []bool{false, true} {
		t.Run(map[bool]string{false: "admission", true: "maintenance"}[maintenance], func(t *testing.T) {
			tenant, now, _ := capacityFixture(t)
			s := tenant.s
			lease := maintenanceReaper(t, s)
			rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','key','expired','run','job',1,?,3)`, now.Add(time.Minute).UnixNano())
			if maintenance {
				rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a','key','next','run','job',1,?,3,'queue')`, now.Add(time.Hour).UnixNano())
			}
			begin, locked := *now, now.Add(2*time.Minute)
			first := true
			s.SetTestClock(t, func() time.Time {
				if first {
					first = false
					return begin
				}
				return locked
			})
			if maintenance {
				result, err := s.AsOperator().MaintainConcurrency(t.Context(), lease, ConcurrencyMaintenanceOptions{})
				if err != nil || len(result.StaleHolders) != 1 || result.Promoted != 1 {
					t.Fatalf("maintenance after lock: %+v, %v", result, err)
				}
			} else {
				result, err := tenant.AcquireConcurrencySlot(t.Context(), AcquireSlotRequest{Key: "key", HolderID: "next", RunID: "run", NodeID: "job", Capacity: 1, Lease: time.Minute})
				if err != nil || result.Kind != AcquireGranted || !result.LeaseExpiresAt.Equal(locked.Add(time.Minute)) {
					t.Fatalf("admission after lock: %+v, %v", result, err)
				}
			}
		})
	}
}
