package store

import (
	"database/sql"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func TestOwnerUnstartedRecoveryFencesPriorAwardsProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(41, 79))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			cloud := i%2 == 0
			s, demand, now := meteredUnitFixture(t, cloud)
			kind, dispatch := capability.UnitTrigger, "host"
			if cloud {
				kind, dispatch = capability.UnitPlan, "controller"
			}
			demand.Target = &UnitRef{Kind: kind, RunID: "run"}
			rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
			rewriteSchemaExec(t, s.db, `UPDATE runs SET status=?,dispatch=?`, []string{"pending", "running"}[i/2%2], dispatch)
			generation := int64(random.IntN(1000) + 1)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind=?,generation=?`, kind, generation)
			tenant := &Tenant{s: s, team: "a"}
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			var previous []UnitClaim
			var charged int64
			for cycle := range random.IntN(3) + 2 {
				claim, _ := meteredUnitClaim(t, s, demand)
				if claim.Award.Generation != generation+int64(cycle)+1 {
					t.Fatalf("generation reset: %+v", claim.Award)
				}
				before, err := tenant.ReadSnapshot(t.Context(), "run")
				if err != nil {
					t.Fatal(err)
				}
				for _, stale := range previous {
					if err := s.ReleaseUnit(t.Context(), stale, stale.Award.CapabilityPrefix); err == nil {
						t.Fatal("former holder released a replacement")
					}
					if _, err := s.RenewUnit(t.Context(), stale.Award.CapabilityPrefix, false); err == nil {
						t.Fatal("former holder renewed a replacement")
					}
					actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: stale.Award.CapabilityPrefix}, CapabilityPrefix: stale.Award.CapabilityPrefix, Owner: stale.Owner}
					finish := FinishRunRequest{Unit: stale.Owner, Transition: RunTransition{RunID: "run", To: RunFailed, Cause: RunFinish, Actor: actor}}
					if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err == nil {
						t.Fatal("former holder finished a replacement's run")
					}
				}
				if reaped, err := s.ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 0 {
					t.Fatalf("live owner recovered: %+v, %v", reaped, err)
				}
				after, err := tenant.ReadSnapshot(t.Context(), "run")
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("refused recovery changed live work: %v", err)
				}
				if !cloud {
					charged += max(int64(limits.HostBillingMinimum/time.Second), int64(claim.Award.LeaseExpiresAt.Sub(*now)/time.Second)) * 2
				}
				*now = claim.Award.LeaseExpiresAt
				for replay := range 2 {
					reaped, err := s.ReapUnits(t.Context(), lease, 10)
					if err != nil || len(reaped) != 1-replay {
						t.Fatalf("recovery %d: %+v, %v", replay, reaped, err)
					}
					if replay == 0 && (!reaped[0].Requeued || reaped[0].Disruptions != 0 || reaped[0].Generation != claim.Award.Generation) {
						t.Fatalf("unstarted award consumed execution: %+v", reaped)
					}
					unitBillingTotal(t, s, charged, true)
				}
				var status, prefix string
				var holder sql.NullString
				var expiry, started sql.NullInt64
				if err := s.db.QueryRow(`SELECT status,claimed_by,capability_prefix,lease_expires_at,started_at FROM triggers`).Scan(&status, &holder, &prefix, &expiry, &started); err != nil || status != "pending" || holder.Valid || prefix != "" || expiry.Valid || started.Valid {
					t.Fatalf("requeue retained authority: %s %v %q %v %v, %v", status, holder, prefix, expiry, started, err)
				}
				rewriteSchemaExec(t, s.db, `UPDATE credit_charges SET charged_at=?`, now.Add(time.Duration(random.IntN(100)+1)*time.Hour).UnixNano())
				previous = append(previous, claim)
			}
			checkRecoveredOwnerFinish(t, s, demand, lease, charged, cloud)
		})
	}
}

func checkRecoveredOwnerFinish(t *testing.T, s *Store, demand UnitDemand, lease SingletonLease, charged int64, cloud bool) {
	t.Helper()
	claim, actor := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: claim.Team}
	finish := FinishRunRequest{Unit: actor.Owner, Transition: RunTransition{RunID: claim.RunID, To: RunSuccess, Cause: RunFinish, Actor: actor}}
	if cloud {
		before, err := tenant.ReadSnapshot(t.Context(), claim.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err == nil {
			t.Fatal("unaccepted planner finished successfully")
		}
		after, err := tenant.ReadSnapshot(t.Context(), claim.RunID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("refused success changed the run: %v", err)
		}
		finish.Transition.To = RunFailed
	}
	for replay := range 2 {
		applied, err := tenant.FinishRunAndUnit(t.Context(), finish)
		if err != nil || applied.Replayed != (replay == 1) {
			t.Fatalf("recovered owner finish: %+v, %v", applied, err)
		}
	}
	if !cloud {
		charged += int64(limits.HostBillingMinimum/time.Second) * 2
	}
	unitBillingTotal(t, s, charged, true)
	before, err := tenant.ReadSnapshot(t.Context(), claim.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if reaped, err := s.ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 0 {
		t.Fatalf("terminal owner recovered: %+v, %v", reaped, err)
	}
	if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
		t.Fatal("terminal run awarded again")
	}
	after, err := tenant.ReadSnapshot(t.Context(), claim.RunID)
	if err != nil || !reflect.DeepEqual(before, after) || after.Run.State != finish.Transition.To {
		t.Fatalf("terminal recovery changed the verdict: %v", err)
	}
	unitBillingTotal(t, s, charged, true)
}
