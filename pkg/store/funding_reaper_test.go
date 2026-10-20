package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestFundingStoppedOwnerAndEvalNeverRequeue(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval} {
		for _, refusal := range []string{"balance", "hold"} {
			t.Run(string(kind)+"/"+refusal, func(t *testing.T) {
				s, claim, _, now := cloudRenewalBillingFixture(t, kind)
				tenant := &Tenant{s: s, team: "a"}
				if _, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true); err != nil {
					t.Fatal(err)
				}
				billingModelJob(t, s, "customer")
				customer, customerActor := meteredUnitClaim(t, s, UnitDemand{CapabilityPrefix: "runner", Target: &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "customer"}})
				if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "customer"}, Actor: customerActor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
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
				beat, err := s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, true)
				if err != nil || !beat.CancelRequested {
					t.Fatalf("funding stop = %+v, %v", beat, err)
				}
				if refusal == "hold" {
					if _, err := tenant.ReleaseCreditFreezes(t.Context(), "funding-stop", "operator", "resolved", *now); err != nil {
						t.Fatal(err)
					}
				}
				rewriteSchemaExec(t, s.db, `UPDATE credit_grants SET amount_micro=1000000`)
				*now = beat.ExpiresAt
				lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				reaped, err := s.ReapUnits(t.Context(), lease, 10)
				if err != nil || len(reaped) != 1 || reaped[0].Requeued || !reaped[0].Failed {
					t.Fatalf("stopped unit recovered = %+v, %v", reaped, err)
				}
				run, err := tenant.ReadRun(t.Context(), "run")
				if err != nil || run.CancelRequestedAt != nil || run.FinishedAt != nil {
					t.Fatalf("funding stop ended independent work = %+v, %v", run, err)
				}
				if renewed, err := s.RenewUnit(t.Context(), customer.Award.CapabilityPrefix, true); err != nil || renewed.CancelRequested {
					t.Fatalf("independent job stopped = %+v, %v", renewed, err)
				}
				if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "customer"}, Actor: customerActor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(1)}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ReapUnits(t.Context(), lease, 10); err != nil {
					t.Fatal(err)
				}
				if kind == capability.UnitPlan {
					_, unusedActor := meteredUnitClaim(t, s, UnitDemand{CapabilityPrefix: "runner", Target: &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}})
					if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: unusedActor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
						t.Fatal(err)
					}
					if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{"run", "job"}, Actor: unusedActor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(1)}); err != nil {
						t.Fatal(err)
					}
					if _, err := s.ReapUnits(t.Context(), lease, 10); err != nil {
						t.Fatal(err)
					}
					finished, err := tenant.ReadRun(t.Context(), "run")
					if err != nil || finished.State != RunFailed {
						t.Fatalf("drained owner did not fail = %+v, %v", finished, err)
					}
				} else {
					source, err := tenant.ReadNode(t.Context(), NodeRef{"run", "job"})
					if err != nil || source.Expansion != ExpansionFailed {
						t.Fatalf("funding stop did not settle expansion = %+v, %v", source, err)
					}
				}
			})
		}
	}
}

func TestFundingStoppedTriggerPreservesReadyJobsAndSettlesDependencies(t *testing.T) {
	for _, scenario := range []struct {
		name                       string
		running, generator, queued bool
	}{{name: "ready"}, {name: "running_and_ready", running: true}, {name: "generator", running: true, generator: true}, {name: "pipeline_queued", queued: true}} {
		t.Run(scenario.name, func(t *testing.T) {
			running := scenario.running
			plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}, {ID: "ready"}, {ID: "next", Needs: []string{"ready"}}}}
			if scenario.queued {
				plan.Concurrency = []nodeproto.ConcurrencySpec{{Group: "pipeline", Scope: new("global"), Capacity: new(1), OnLimit: new("queue")}}
			}
			if scenario.generator {
				plan.Jobs[0].Closures.Generate = []string{"group"}
				plan.Jobs[2].NeedsGenerated = []string{"group"}
			}
			tenant, request := sharedSettingsPlan(t, plan)
			s := tenant.s
			localRequest := request.Seal.Request
			localRequest.Request.LocalSecretNames = []string{"KEY"}
			if _, err := s.installLocalSecrets(tenant.team, AdmissionResult{RunID: request.RunID, Request: localRequest}, map[string]string{"KEY": "requester-value"}); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.TransitionRun(t.Context(), RunTransition{RunID: request.RunID, To: RunRunning, Cause: RunStart, Actor: request.Actor}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"job", "ready"} {
				if scenario.queued {
					continue
				}
				if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{request.RunID, id}, To: NodeReady, Cause: CauseRelease, Actor: request.Actor}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.queued {
				maintenanceRun(t, s, "competitor", 200, RunRunning)
				other := authorityRoot("competitor-owner", "a")
				other.Unit = capability.UnitTrigger
				other.Grants = []capability.Grant{capability.NodeResult}
				other.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: "competitor", Holder: "competitor-owner", Generation: 1}
				persistPlanOwner(t, s, other)
				rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels) VALUES ('a','competitor','claimed','trigger',1,1,'competitor-owner','competitor-owner',1,?,?,?)`, s.now().Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
				otherActor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: other.Prefix}, CapabilityPrefix: other.Prefix, Owner: OwnerLeaseRef{Kind: OwnerTrigger, RunID: "competitor", Holder: "competitor-owner", Generation: 1}}
				occupied, err := tenant.AcquireCapacity(t.Context(), AcquireSlotRequest{Key: "global:pipeline", HolderID: "competitor/-", RunID: "competitor", Capacity: 1, Cost: 1, Policy: OnLimitQueue}, CapacityBinding{RunID: "competitor", Generation: 1}, otherActor)
				if err != nil || occupied.Kind != AcquireGranted {
					t.Fatalf("competing pipeline = %+v, %v", occupied, err)
				}
				queued, err := tenant.AcquireCapacity(t.Context(), AcquireSlotRequest{Key: "global:pipeline", HolderID: request.RunID + "/-", RunID: request.RunID, Capacity: 1, Cost: 1, Policy: OnLimitQueue}, CapacityBinding{RunID: request.RunID, Generation: request.Actor.Owner.Generation}, request.Actor)
				if err != nil || queued.Kind != AcquireQueued {
					t.Fatalf("pipeline did not queue before funding stop = %+v, %v", queued, err)
				}
			}
			capacityAcquired := s.now()
			capacity, err := tenant.AcquireCapacity(t.Context(), AcquireSlotRequest{Key: "global:carry", HolderID: "carry", RunID: request.RunID, Capacity: 1, Cost: 1}, CapacityBinding{RunID: request.RunID, Generation: request.Actor.Owner.Generation}, request.Actor)
			if err != nil || capacity.Kind != AcquireGranted {
				t.Fatalf("host run capacity = %+v, %v", capacity, err)
			}
			root := authorityRoot("customer", "a")
			root.Grants = []capability.Grant{capability.NodesClaim}
			authorityPersist(t, s, root)
			start := func(id string) Actor {
				t.Helper()
				_, actor := meteredUnitClaim(t, s, UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: request.RunID, NodeID: id}})
				if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{request.RunID, id}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
					t.Fatal(err)
				}
				return actor
			}
			var first Actor
			if running {
				first = start("job")
			}
			now := s.now()
			s.clock = func() time.Time { return now }
			fundUnitClaim(t, s)
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				owner, err := loadCapabilityTx(t.Context(), tx, request.Actor.CapabilityPrefix)
				if err != nil {
					return err
				}
				owner.Billing.Metered = true
				return recordBillingAwardTx(t.Context(), tx, owner, CreditRate{Cores: 1, MicroPerSecond: 2}, SliceSeconds)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.TestOnlyHoldForDispute(t.Context(), "trigger-stop", "", "probe", now); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.ReleaseCreditFreezes(t.Context(), "trigger-stop", "operator", "resolved", now); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET lease_expires_at=? WHERE team='a' AND id=?`, now.UnixNano(), request.RunID)
			rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','previous',?,6)`, now.Add(-time.Second).UnixNano())
			controller, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "controller", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if controller.Generation == request.Actor.Owner.Generation {
				t.Fatal("controller generation must differ from stopped host generation")
			}
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReapUnits(t.Context(), lease, 10); err != nil {
				t.Fatal(err)
			}
			ready, err := tenant.ReadNode(t.Context(), NodeRef{request.RunID, "ready"})
			if scenario.queued {
				if err != nil || ready.ReadyAt != nil || ready.Status == StatusDone {
					t.Fatalf("funding-stop takeover bypassed queued pipeline admission: status=%s outcome=%s ready=%v, %v", ready.Status, ready.Outcome, ready.ReadyAt, err)
				}
				state, err := tenant.GetConcurrencyState(t.Context(), "global:pipeline")
				if err != nil || len(state.Waiters) != 1 || state.Waiters[0].RunID != request.RunID {
					t.Fatalf("funding-stop takeover lost original pipeline waiter = %+v, %v", state, err)
				}
				return
			}
			if err != nil || ready.CancelRequestedAt != nil || ready.Status == StatusDone || ready.ReadyAt == nil {
				t.Fatalf("independent ready job lost = %+v, %v", ready, err)
			}
			if _, err := s.AsOperator().ReconcileControllerRuns(t.Context(), controller, "", 10); err != nil {
				t.Fatal(err)
			}
			if value, err := s.localSecret(tenant.team, request.RunID, "KEY"); err != nil || value != "requester-value" {
				t.Fatalf("requester secret lost during takeover: %v", err)
			}
			controllerActor := Actor{Kind: ActorController, Owner: OwnerLeaseRef{Kind: OwnerController, Name: controller.Name, Holder: controller.Holder, Generation: controller.Generation, RunID: request.RunID}}
			adopted, err := tenant.AdoptCapacity(t.Context(), "global:carry", "carry", CapacityBinding{RunID: request.RunID, Generation: controller.Generation}, controllerActor)
			if err != nil || adopted.Kind != AdoptHolder || adopted.Holder.Binding.Generation != controller.Generation || !adopted.Holder.AcquiredAt.Equal(capacityAcquired) {
				t.Fatalf("continuous run capacity lost during takeover = %+v, %v", adopted, err)
			}
			for _, id := range []string{"job", "ready", "next"} {
				actor := first
				if id != "job" || !running {
					actor = start(id)
				}
				if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{request.RunID, id}, Actor: actor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(1)}); err != nil {
					t.Fatal(err)
				}
				if scenario.generator && id == "job" {
					eval, actor := meteredUnitClaim(t, s, UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitEval, RunID: request.RunID, NodeID: "job", SourceGeneration: 1}})
					if beat, err := s.RenewUnit(t.Context(), eval.Award.CapabilityPrefix, true); err != nil || beat.CancelRequested {
						t.Fatalf("generator evaluation after takeover = %+v, %v", beat, err)
					}
					actor.Kind = ActorRunHost
					if _, err := tenant.AcceptExpansion(t.Context(), ExpansionSubmission{Source: NodeRef{request.RunID, "job"}, SourceGeneration: 1, Actor: actor, Body: []byte(`{"jobs":[]}`)}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.ReapUnits(t.Context(), lease, 10); err != nil {
					t.Fatal(err)
				}
			}
			run, err := tenant.ReadRun(t.Context(), request.RunID)
			if err != nil || run.State != RunSuccess || run.CancelRequestedAt != nil || run.Owner == nil || run.Owner.Kind != OwnerController || run.Dispatch != "host" {
				t.Fatalf("dependency settlement did not finish the accepted graph = %+v, %v", run, err)
			}
			if _, err := s.localSecret(tenant.team, request.RunID, "KEY"); err == nil {
				t.Fatal("requester secret retained after the run finished")
			}
			if state, err := tenant.GetConcurrencyState(t.Context(), "global:carry"); err != nil || len(state.Holders) != 0 {
				t.Fatalf("run capacity retained after finish = %+v, %v", state, err)
			}
		})
	}
}
