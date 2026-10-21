package store

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func cloudRenewalBillingFixture(t *testing.T, kind capability.UnitKind) (*Store, UnitClaim, Actor, *time.Time) {
	t.Helper()
	s, demand, now := meteredUnitFixture(t, true)
	if kind != capability.UnitNode {
		demand.Target = &UnitRef{Kind: kind, RunID: "run"}
		rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
		rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
	}
	if kind == capability.UnitEval {
		demand.Target.NodeID, demand.Target.SourceGeneration = "job", 7
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected'`)
		rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','controller',?,3)`, now.Add(time.Hour).UnixNano())
		rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES ('a','run','job',7,'pending',3)`)
	}
	claim, actor := meteredUnitClaim(t, s, demand)
	return s, claim, actor, now
}

func TestCloudPlanIdleRenewalIsCharged(t *testing.T) {
	s, claim, _, now := cloudRenewalBillingFixture(t, capability.UnitPlan)
	*now = now.Add(5 * time.Second)
	if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(11 * time.Second)
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	assertCloudRenewalCharge(t, s, claim, 22)
}

func TestCloudRenewalBillsHeldTimeRegardlessOfActivityProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(1010, 4))
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		for sample := range 10 {
			reports := uint8(random.Uint32())
			if sample < 2 {
				reports = uint8(sample * 255)
			}
			t.Run(fmt.Sprintf("%s/%08b", kind, reports), func(t *testing.T) {
				s, claim, actor, now := cloudRenewalBillingFixture(t, kind)
				*now = now.Add(5 * time.Second)
				if kind == capability.UnitNode && reports != 0 {
					if _, err := (&Tenant{s: s, team: claim.Team}).TransitionNode(t.Context(), nodeTransition{Ref: controllerwire.NodeRef{RunID: claim.RunID, NodeID: claim.NodeID}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
						t.Fatal(err)
					}
				}
				start := *now
				var beat LeaseBeat
				for renewal := range 8 {
					executing := reports&(1<<renewal) != 0
					var err error
					beat, err = s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, executing)
					if err != nil {
						t.Fatal(err)
					}
					*now = now.Add(10 * time.Second)
				}
				*now = beat.ExpiresAt
				lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				if reaped, err := s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 1 {
					t.Fatalf("reaped=%+v, %v", reaped, err)
				}
				heldSeconds := int64(beat.ExpiresAt.Sub(start) / time.Second)
				want := heldSeconds * 2
				if kind == capability.UnitNode {
					want = max(60, heldSeconds) * 10
				}
				assertCloudRenewalCharge(t, s, claim, want)
			})
		}
	}
}

func assertCloudRenewalCharge(t *testing.T, s *Store, claim UnitClaim, want int64) {
	t.Helper()
	charges, err := (&Tenant{s: s, team: claim.Team}).ListCreditCharges(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, charge := range charges {
		total += charge.AmountMicro
	}
	if total != want {
		t.Fatalf("Cloud %s charge=%d; want %d", claim.Kind, total, want)
	}
}
