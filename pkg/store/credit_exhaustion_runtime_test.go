package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestMeteredUnitExhaustionCancelsAfterGraceProperty(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitNode, capability.UnitPlan, capability.UnitEval, capability.UnitTrigger} {
		for _, restore := range []bool{false, true} {
			t.Run(string(kind)+map[bool]string{false: "/exhausted", true: "/restored"}[restore], func(t *testing.T) {
				s, demand, now := meteredUnitFixture(t, kind != capability.UnitTrigger)
				demand.Target = &UnitRef{Kind: kind, RunID: "run"}
				switch kind {
				case capability.UnitNode:
					demand.Target.NodeID = "job"
				case capability.UnitPlan, capability.UnitEval:
					rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
					rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
					if kind == capability.UnitEval {
						demand.Target.NodeID, demand.Target.SourceGeneration = "job", 7
						rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,generation=7,expansion='expected'`)
						rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','controller',?,3)`, now.Add(time.Hour).UnixNano())
						rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES ('a','run','job',7,'pending',3)`)
					}
				}
				claim, _ := meteredUnitClaim(t, s, demand)
				s.billing = nil
				rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET amount_micro=0 WHERE team='a'`)
				began := *now
				for tick := 0; tick <= 61; tick++ {
					*now = began.Add(time.Duration(tick) * 10 * time.Second)
					if restore && tick == 30 {
						if _, err := (&Tenant{s: s, team: "a"}).RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1_000_000, Reference: "restore"}); err != nil {
							t.Fatal(err)
						}
					}
					beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, kind != capability.UnitNode)
					wantCancel := !restore && tick >= 60
					if err != nil || beat.CancelRequested != wantCancel || (wantCancel && beat.CancelReason != EventKindCreditsExhausted) {
						t.Fatalf("tick=%d cancel=%t: %+v, %v", tick, wantCancel, beat, err)
					}
				}
			})
		}
	}
}

func TestMeteredUnitGrantsCannotRestartGraceUnderSpendCap(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, true)
	claim, _ := meteredUnitClaim(t, s, demand)
	tenant := &Tenant{s: s, team: claim.Team}
	if err := tenant.SetSpendBudget(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE team_spend_days SET amount_micro=? WHERE team='a'", MicroCreditsPerCent)
	began := *now
	for tick := 0; tick <= 60; tick++ {
		*now = began.Add(time.Duration(tick) * 10 * time.Second)
		if tick == 30 {
			if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 1_000_000, Reference: "capped"}); err != nil {
				t.Fatal(err)
			}
		}
		beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false)
		if err != nil || beat.CancelRequested != (tick == 60) {
			t.Fatalf("tick=%d capped renewal: %+v, %v", tick, beat, err)
		}
	}
}
