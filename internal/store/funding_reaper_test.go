package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestFundingStoppedEvalNeverRequeues(t *testing.T) {
	for _, refusal := range []string{"balance", "hold"} {
		t.Run(refusal, func(t *testing.T) {
			s, claim, _, now := cloudRenewalBillingFixture(t, capability.UnitEval)
			tenant := &Tenant{s: s, team: "a"}
			if _, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err != nil {
				t.Fatal(err)
			}
			billingModelJob(t, s, "customer")
			customer, customerActor := meteredUnitClaim(t, s, UnitDemand{CapabilityPrefix: "runner", Labels: []string{"team-4cpu"}, Target: &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "customer"}})
			admitExecution(t, tenant, customerActor)
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "customer"}, Actor: customerActor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(10 * time.Second)
			if refusal == "hold" {
				if _, err := tenant.TestOnlyHoldForDispute(t.Context(), "funding-stop", "", "dispute opened", *now); err != nil {
					t.Fatal(err)
				}
			} else {
				rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET amount_micro=120`)
			}
			beat, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
			if err != nil || !beat.CancelRequested {
				t.Fatalf("funding stop = %+v, %v", beat, err)
			}
			if refusal == "hold" {
				s.SetTestClock(t, func() time.Time { return *now })
				if _, err := tenant.ReleaseCreditFreezes(t.Context(), "funding-stop", "operator", "resolved"); err != nil {
					t.Fatal(err)
				}
			}
			rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET amount_micro=1000000`)
			*now = beat.ExpiresAt
			reaped, err := s.AsOperator().ReapUnits(t.Context(), 10)
			if err != nil || len(reaped) != 1 || reaped[0].Requeued || !reaped[0].Failed {
				t.Fatalf("stopped unit recovered = %+v, %v", reaped, err)
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || run.CancelRequestedAt != nil || run.FinishedAt != nil {
				t.Fatalf("funding stop ended independent work = %+v, %v", run, err)
			}
			if renewed, err := s.AsOperator().RenewUnit(t.Context(), customer.Award.CapabilityPrefix, true); err != nil || renewed.CancelRequested {
				t.Fatalf("independent job stopped = %+v, %v", renewed, err)
			}
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "customer"}, Actor: customerActor, To: runstate.NodeSuccess, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(1)}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AsOperator().ReapUnits(t.Context(), 10); err != nil {
				t.Fatal(err)
			}
			source, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || source.Expansion != runstate.ExpansionFailed {
				t.Fatalf("funding stop did not settle expansion = %+v, %v", source, err)
			}
		})
	}
}

func TestFundingStoppedOwnerCancelsWholeRun(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan} {
		for _, refusal := range []string{"balance", "hold"} {
			t.Run(string(kind)+"/"+refusal, func(t *testing.T) {
				s, demand, now := meteredUnitFixture(t, kind == capability.UnitPlan)
				demand.Target = &UnitRef{Kind: kind, RunID: "run"}
				if kind == capability.UnitPlan {
					rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
					rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
				}
				owner, _ := meteredUnitClaim(t, s, demand)
				tenant := &Tenant{s: s, team: "a"}
				if _, err := s.AsOperator().RenewUnit(t.Context(), owner.Award.CapabilityPrefix, true); err != nil {
					t.Fatal(err)
				}
				billingModelJob(t, s, "customer")
				customer, actor := meteredUnitClaim(t, s, UnitDemand{CapabilityPrefix: "runner", Labels: []string{"team-4cpu"}, Target: &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "customer"}})
				admitExecution(t, tenant, actor)
				if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "customer"}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
					t.Fatal(err)
				}
				*now = now.Add(10 * time.Second)
				if refusal == "hold" {
					if _, err := tenant.TestOnlyHoldForDispute(t.Context(), "owner-stop", "", "dispute opened", *now); err != nil {
						t.Fatal(err)
					}
				} else {
					rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET amount_micro=120`)
				}
				if beat, err := s.AsOperator().RenewUnit(t.Context(), owner.Award.CapabilityPrefix, true); err != nil || !beat.CancelRequested {
					t.Fatalf("owner funding refusal = %+v, %v", beat, err)
				}
				run, err := tenant.ReadRun(t.Context(), "run")
				if err != nil || run.CancelRequestedAt == nil || run.CancelReason == "" {
					t.Fatalf("owner stop did not durably cancel run: state=%s cancelled=%v reason=%q, %v", run.State, run.CancelRequestedAt, run.CancelReason, err)
				}
				ready, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
				if err != nil || ready.Status != runstate.StatusDone || ready.Outcome != runstate.NodeCancelled {
					t.Fatalf("owner stop left ready work = %+v, %v", ready, err)
				}
				beat, err := s.AsOperator().RenewUnit(t.Context(), customer.Award.CapabilityPrefix, true)
				if err != nil || !beat.CancelRequested {
					t.Fatalf("owner stop spared customer job = %+v, %v", beat, err)
				}
				if refusal == "hold" {
					s.SetTestClock(t, func() time.Time { return *now })
					if _, err := tenant.ReleaseCreditFreezes(t.Context(), "owner-stop", "operator", "resolved"); err != nil {
						t.Fatal(err)
					}
				}
				rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET amount_micro=1000000`)
				// Lose both host connections. The reaper must drain the cancelled graph without taking it over.
				*now = now.Add(4 * time.Minute)
				if _, err := s.AsOperator().ReapUnits(t.Context(), 10); err != nil {
					t.Fatal(err)
				}
				run, err = tenant.ReadRun(t.Context(), "run")
				if err != nil || run.State != runstate.RunCancelled || run.FinishedAt == nil {
					t.Fatalf("owner stop did not finish run = %+v, %v", run, err)
				}
				var unsettled int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM billing_awards WHERE team='a' AND settled_at IS NULL`).Scan(&unsettled); err != nil || unsettled != 0 {
					t.Fatalf("owner stop retained %d billing awards: %v", unsettled, err)
				}
			})
		}
	}
}
