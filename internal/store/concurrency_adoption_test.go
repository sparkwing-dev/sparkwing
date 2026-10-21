package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func capacityFixture(t *testing.T) (*Tenant, *time.Time, Actor) {
	t.Helper()
	s, tenant, now := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, RunRunning)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch = 'controller',plan_accepted_generation=1,sealed_at=1,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan' WHERE team = 'a' AND id = 'run'`, []byte(`{}`), []byte(`{"jobs":[{"id":"job"}]}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','owner',?,2)`, now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,generation,claimed_by,capability_prefix,lease_expires_at) VALUES ('a','run','job','pending',?,?,3,'machine','machine',?)`, []byte(`{"id":"job"}`), []byte(`{"concurrency":[{"group":"key","scope":"global","capacity":1,"cost":1,"on_limit":"queue"}]}`), now.Add(time.Minute).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a','job:global:key',1,1),('a','key',1,1)`)
	actor := Actor{Kind: ActorController, Owner: OwnerLeaseRef{Kind: OwnerController, Name: "controller.owner", EvalFence: runspec.EvalFence{Holder: "owner", Generation: 2}}}
	return tenant, now, actor
}

func TestCapacityRenewalUsesClaimOrOwnerLiveness(t *testing.T) {
	for _, ownerLive := range []bool{false, true} {
		t.Run(map[bool]string{true: "owner", false: "claim"}[ownerLive], func(t *testing.T) {
			tenant, now, _ := capacityFixture(t)
			started := *now
			if !ownerLive {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET until=?`, now.Add(time.Minute).UnixNano())
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET lease_expires_at=?`, now.Add(time.Hour).UnixNano())
			}
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','job:global:key','holder','run','job',?,?,3)`, now.UnixNano(), now.Add(time.Minute).UnixNano())
			for _, elapsed := range []time.Duration{30 * time.Second, 90 * time.Second} {
				*now = started.Add(elapsed)
				want := now.Add(3 * time.Minute)
				err := tenant.s.inTx(t.Context(), func(tx *storeTx) error {
					return renewCapacityTx(t.Context(), tx, tenant.team, "run", "job", want)
				})
				var expires int64
				if err != nil {
					t.Fatal(err)
				}
				if err := tenant.s.db.QueryRow(`SELECT lease_expires_at FROM concurrency_holders`).Scan(&expires); err != nil || expires != want.UnixNano() {
					t.Fatalf("renewal at %s = %v, want %v: %v", elapsed, time.Unix(0, expires), want, err)
				}
			}
		})
	}
}

func TestCapacityRenewalOwnerCannotExtendFunding(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, false)
	claim, _ := meteredUnitClaim(t, s, demand)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller',plan_accepted_generation=1 WHERE team='a' AND id='run'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','owner',?,2)`, now.Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a','job:global:key',1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a','job:global:key','holder','run','job',?,?,?)`, now.UnixNano(), claim.Award.LeaseExpiresAt.UnixNano(), claim.Award.Generation)
	*now = now.Add(5 * time.Second)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		return renewCapacityTx(t.Context(), tx, "a", "run", "job", now.Add(3*time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	var expires int64
	if err := s.db.QueryRow(`SELECT lease_expires_at FROM concurrency_holders`).Scan(&expires); err != nil || expires != claim.Award.LeaseExpiresAt.UnixNano() {
		t.Fatalf("capacity funding bound = %v, want %v: %v", time.Unix(0, expires), claim.Award.LeaseExpiresAt, err)
	}
}
