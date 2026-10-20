package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func launcherMeteredFence(t *testing.T, s *Store, demand UnitDemand, claim UnitClaim, kind capability.UnitKind) (capability.AuthorityRef, capability.LeaseRef) {
	t.Helper()
	root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	awards, err := s.ReadLauncherAwards(t.Context(), root, nil)
	if err != nil || len(awards) != 1 {
		t.Fatalf("launcher awards: %+v %v", awards, err)
	}
	return root, capability.LeaseRef{Kind: kind, ID: awards[0].Capability.UnitID, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
}

func chargedMicro(t *testing.T, s *Store) int64 {
	t.Helper()
	var amount int64
	if err := s.db.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(amount_micro),0) FROM credit_charges`).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	return amount
}

func TestLauncherAwardMetersFromFirstPodRenewal(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, claim, now := launcherSnapshotFixture(t, kind)
			root, fence := launcherMeteredFence(t, s, demand, claim, kind)
			reserved := chargedMicro(t, s)
			for range 6 {
				*now = now.Add(SliceSeconds * time.Second * 2 / 3)
				if prepared, err := s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence}); err != nil || len(prepared) != 1 {
					t.Fatalf("preparation: %+v %v", prepared, err)
				}
			}
			rewriteSchemaCount(t, s.db, "billable_intervals", 0)
			if got := chargedMicro(t, s); got != reserved {
				t.Fatalf("preparation charged %d micro beyond the %d reservation", got-reserved, reserved)
			}
			issued, err := s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
			if err != nil {
				t.Fatal(err)
			}
			*now = now.Add(10 * time.Second)
			if _, err = s.RenewUnit(t.Context(), issued.Capability.Prefix, false); err != nil {
				t.Fatal(err)
			}
			var began int64
			if err = s.db.QueryRowContext(t.Context(), `SELECT started_at FROM billable_intervals WHERE finished_at IS NULL`).Scan(&began); err != nil || began != now.UnixNano() {
				t.Fatalf("metering began at %d, %v; want the pod's first renewal %d", began, err, now.UnixNano())
			}
		})
	}
}

func TestCancelledUnstartedLauncherAwardEndsMetering(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, claim, now := launcherSnapshotFixture(t, kind)
			root, fence := launcherMeteredFence(t, s, demand, claim, kind)
			*now = now.Add(time.Minute)
			if _, err := s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence}); err != nil {
				t.Fatal(err)
			}
			operator := authorityMember(t, s, "operator", RoleOwner)
			operator.Kind = ActorOperator
			*now = now.Add(time.Second)
			if _, err := (&Tenant{s: s, team: "a"}).TransitionRun(t.Context(), RunTransition{RunID: "run", To: RunCancelled, Cause: RunCancel, Actor: operator, Reason: "user_cancelled"}); err != nil {
				t.Fatal(err)
			}
			unitBillingTotal(t, s, 0, true)
			rewriteSchemaCount(t, s.db, "billable_intervals", 0)
			*now = now.Add(time.Second)
			prepared, err := s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence})
			if err != nil || len(prepared) != 0 {
				t.Fatalf("cancelled preparation renewed: %+v %v", prepared, err)
			}
			if _, err = s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix}); err == nil {
				t.Fatal("cancelled unstarted award exchanged authority")
			}
			unitBillingTotal(t, s, 0, true)
		})
	}
}

// A launcher award whose Job never starts, because its boundary check keeps
// failing, holds the work minimum as a reservation and must refund it in full
// whether its lease lapses, the run is cancelled, or both.
func TestUnstartedLauncherAwardRefundsItsReservation(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		for _, path := range []string{"lapse", "renewed_lapse", "lapse_then_cancel"} {
			t.Run(string(kind)+"/"+path, func(t *testing.T) {
				s, demand, claim, now := launcherSnapshotFixture(t, kind)
				root, fence := launcherMeteredFence(t, s, demand, claim, kind)
				if chargedMicro(t, s) == 0 {
					t.Fatal("award reserved nothing")
				}
				lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", 10*time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if path == "renewed_lapse" {
					for range 3 {
						*now = now.Add(limits.DefaultLease / 2)
						if _, err := s.RenewLauncherPreparation(t.Context(), root, []capability.LeaseRef{fence}); err != nil {
							t.Fatal(err)
						}
					}
				}
				*now = now.Add(limits.DefaultLease * 2)
				if _, err := s.ReapUnits(t.Context(), lease, 10); err != nil {
					t.Fatal(err)
				}
				if path == "lapse_then_cancel" {
					operator := authorityMember(t, s, "operator", RoleOwner)
					operator.Kind = ActorOperator
					*now = now.Add(time.Second)
					if _, err := (&Tenant{s: s, team: "a"}).TransitionRun(t.Context(), RunTransition{RunID: "run", To: RunCancelled, Cause: RunCancel, Actor: operator, Reason: "user_cancelled"}); err != nil {
						t.Fatal(err)
					}
				}
				unitBillingTotal(t, s, 0, true)
				rewriteSchemaCount(t, s.db, "billable_intervals", 0)
			})
		}
	}
}
