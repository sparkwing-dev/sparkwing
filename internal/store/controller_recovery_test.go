package store

import (
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func controllerPlanFixture(t *testing.T, plan nodeproto.PlanDoc, prepare ...func(*Tenant, *PlanAcceptance)) (*Tenant, PlanAcceptance, *time.Time) {
	t.Helper()
	tenant, request, now := planFixture(t, runspec.DispatchController)
	var err error
	request.Seal.Plan, err = json.Marshal(testBodyPlan(plan))
	if err != nil {
		t.Fatal(err)
	}
	request.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Declarations: runspec.CheckoutDeclarations{Version: 1}, Request: request.Seal.Request, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	for _, setup := range prepare {
		setup(tenant, &request)
	}
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	return tenant, request, now
}

func controllerJobAward(t *testing.T, tenant *Tenant, runID string) runstate.Actor {
	t.Helper()
	root := authorityRoot("runner", "a")
	root.Grants = []capability.Grant{capability.NodesClaim}
	authorityPersist(t, tenant.s, root)
	demand := UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(runID), NodeID: "job"}}
	fundUnitClaim(t, tenant.s)
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
	return actor
}

func controllerJobActor(t *testing.T, tenant *Tenant, runID string) runstate.Actor {
	t.Helper()
	actor := controllerJobAward(t, tenant, runID)
	admitExecution(t, tenant, actor)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(runID), NodeID: "job"}, To: runstate.NodeRunning, Cause: runstate.CauseStart, Actor: actor, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	return actor
}

func TestControllerReconcileResumesReports(t *testing.T) {
	tenant, request, _ := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}, {ID: "next", Needs: []string{"job"}}}})
	actor := controllerJobActor(t, tenant, request.RunID)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"}, To: runstate.NodeSuccess, Cause: runstate.CauseReport, Actor: actor, SourceAttempt: sourceAttempt(1)}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		visited, err := tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10)
		if err != nil || len(visited) != 1 || visited[0].Team != tenant.team || visited[0].RunID != request.RunID {
			t.Fatalf("reconcile: %+v, %v", visited, err)
		}
	}
	next, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "next"})
	if err != nil || next.ReadyAt == nil || next.StateSeq != 1 {
		t.Fatalf("release: %+v, %v", next, err)
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.State != runstate.RunRunning || run.Owner == nil || run.Owner.Kind != runstate.OwnerController {
		t.Fatalf("controller owner: %+v, %v", run, err)
	}
}

func TestControllerReconcilePagesAllTeamsAndFinishesAtomically(t *testing.T) {
	s, _, _ := maintenanceStore(t)
	for i, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		maintenanceRun(t, s, id, i+1, runstate.RunPending)
		rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller',sealed_at=1,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan',plan_accepted_generation=1 WHERE id=?`, []byte(`{}`), []byte(`{"jobs":[]}`), id)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET team='b' WHERE id='c'`)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='host' WHERE id='b'`)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET intake_mode='publish' WHERE id='d'`)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET sealed_at=NULL,spec_json=NULL,spec_hash='',plan_json=NULL,plan_hash='' WHERE id='e'`)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET plan_accepted_generation=0 WHERE id='f'`)
	var visited []ControllerRun
	after := ""
	for {
		page, err := s.AsOperator().ReconcileControllerRuns(t.Context(), after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		visited = append(visited, page...)
		after = page[len(page)-1].RunID
	}
	if len(visited) != 3 || visited[0] != (ControllerRun{Team: "a", RunID: "a"}) || visited[1] != (ControllerRun{Team: "b", RunID: "c"}) || visited[2].RunID != "g" {
		t.Fatalf("visited: %+v", visited)
	}
	for _, ref := range visited {
		tenant := &Tenant{s: s, team: ref.Team}
		run, err := tenant.ReadRun(t.Context(), ref.RunID)
		if err != nil || run.State != runstate.RunSuccess {
			t.Fatalf("finished: %+v, %v", run, err)
		}
		events, err := tenant.ReadStateEvents(t.Context(), ref.RunID, 0, 10)
		if err != nil || len(events) != 2 || events[0].Kind != "run_started" || events[1].Kind != "run_success" {
			t.Fatalf("events: %+v, %v", events, err)
		}
	}
	rewriteSchemaCount(t, s.db, "run_effects", 12)
	for _, id := range []string{"b", "d", "e", "f"} {
		run, err := (&Tenant{s: s, team: "a"}).ReadRun(t.Context(), id)
		if err != nil || run.State != runstate.RunPending || run.StateSeq != 0 {
			t.Fatalf("ineligible run changed: %+v, %v", run, err)
		}
	}
}

func TestControllerReconcileRejectsInvalidLimitWithoutRuns(t *testing.T) {
	s, _, _ := maintenanceStore(t)
	if _, err := s.AsOperator().ReconcileControllerRuns(t.Context(), "", 0); err == nil {
		t.Fatal("invalid controller pass succeeded")
	}
}

func TestControllerReconcileRollsBackFailedCompletion(t *testing.T) {
	s, tenant, _ := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, runstate.RunRunning)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller',sealed_at=1,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan',plan_accepted_generation=1`, []byte(`{}`), []byte(`{"jobs":[]}`))
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_completion BEFORE INSERT ON run_effects BEGIN SELECT RAISE(ABORT,'completion unavailable'); END`)
	if visited, err := s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10); err == nil || len(visited) != 0 {
		t.Fatalf("failed completion: %+v, %v", visited, err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != runstate.RunRunning || run.StateSeq != 0 || run.FinishedAt != nil {
		t.Fatalf("partial completion: %+v, %v", run, err)
	}
	rewriteSchemaCount(t, s.db, "events", 0)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_completion`)
	if _, err := s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10); err != nil {
		t.Fatal(err)
	}
	run, err = tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != runstate.RunSuccess {
		t.Fatalf("completion retry: %+v, %v", run, err)
	}
}

func TestControllerRetryKeepsPlannerOwnershipUntilHandoff(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "unstarted", true: "disruption_limit"}[started], func(t *testing.T) {
			tenant, request, now := planFixture(t, runspec.DispatchController)
			accepted, err := tenant.AcceptPlan(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			retry, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"}, Request: runspec.Request{Pipeline: "p", Lineage: runspec.Lineage{RetryOf: request.RunID}}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), retry.RunID)
			if err != nil || run.Owner == nil || run.Owner.Kind != runstate.OwnerTrigger || run.Spec == nil || run.Spec.Hash != accepted.Spec.Hash {
				t.Fatalf("retry owner/seal: %+v, %v", run, err)
			}
			controller := controllerActor(retry.RunID)
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(retry.RunID), NodeID: "job"}, To: runstate.NodeReady, Cause: runstate.CauseRelease, Actor: controller}); err == nil {
				t.Fatal("controller bypassed retry planner")
			}
			visited, err := tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10)
			if err != nil || len(visited) != 1 || visited[0].RunID != request.RunID {
				t.Fatalf("retry settled before handoff: %+v, %v", visited, err)
			}
			root := authorityRoot("launcher", "")
			root.Subject.Kind = capability.SubjectService
			root.Grants = []capability.Grant{capability.ClaimsLaunch}
			authorityPersist(t, tenant.s, root)
			demand := UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitPlan, RunID: runspec.RunID(retry.RunID)}}
			fundUnitClaim(t, tenant.s)
			claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			if started {
				issued, err := tenant.s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tenant.s.AsOperator().RenewUnit(t.Context(), issued.Capability.Prefix, true); err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, tenant.s.db, "UPDATE triggers SET disruptions=2 WHERE team=? AND id=?", tenant.team, retry.RunID)
			}
			*now = claim.Award.LeaseExpiresAt
			reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), 10)
			if err != nil || len(reaped) != 1 || reaped[0].RunID != retry.RunID || reaped[0].Requeued == started || reaped[0].Failed != started {
				t.Fatalf("retry planner loss: %+v, %v", reaped, err)
			}
			run, err = tenant.ReadRun(t.Context(), retry.RunID)
			want := runstate.RunPending
			if started {
				want = runstate.RunFailed
			}
			if err != nil || run.State != want || run.Spec.Hash != accepted.Spec.Hash {
				t.Fatalf("recovered retry: %+v, %v", run, err)
			}
			if started {
				return
			}
			claim, err = tenant.s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			issued, err := tenant.s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
			if err != nil {
				t.Fatal(err)
			}
			request.RunID = retry.RunID
			request.Actor = runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix, Owner: claim.Owner}
			resumed, err := tenant.AcceptPlan(t.Context(), request)
			if err != nil || !resumed.Replayed || resumed.Owner == nil || resumed.Owner.Kind != runstate.OwnerController {
				t.Fatalf("retry handoff: %+v, %v", resumed, err)
			}
		})
	}
}

func TestControllerReconcileRestoresExpansionAndApprovalWaits(t *testing.T) {
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "job", Closures: nodeproto.ClosureSpec{Generate: []string{"group"}}},
		{ID: "gate", Approval: &nodeproto.ApprovalSpec{}},
	}}
	tenant, request, now := controllerPlanFixture(t, plan)
	actor := controllerJobActor(t, tenant, request.RunID)
	before, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "gate"})
	if err != nil || before.Status != runstate.StatusApprovalPending {
		t.Fatalf("initial approval: %+v, %v", before, err)
	}
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"}, To: runstate.NodeSuccess, Cause: runstate.CauseReport, Actor: actor, SourceAttempt: sourceAttempt(1)}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), 10); err != nil || len(reaped) != 0 {
		t.Fatalf("owner lapse failed waiting work: %+v, %v", reaped, err)
	}
	for range 2 {
		if _, err := tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSchemaCount(t, tenant.s.db, "eval_units", 1)
	gate, err := tenant.ReadNode(t.Context(), before.Ref)
	if err != nil || gate.Status != runstate.StatusApprovalPending || gate.StateSeq != before.StateSeq {
		t.Fatalf("approval changed during takeover: %+v, %v", gate, err)
	}
	root := authorityRoot("launcher", "")
	root.Subject.Kind = capability.SubjectService
	root.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, tenant.s, root)
	fundUnitClaim(t, tenant.s)
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitEval, RunID: runspec.RunID(request.RunID), NodeID: "job", SourceGeneration: 1}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := tenant.s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tenant.AcceptExpansion(t.Context(), ExpansionSubmission{Registration: runspec.SecretRegistration{Version: 1}, Source: runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"}, SourceGeneration: 1, Actor: runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix, Owner: claim.Owner}, Body: []byte("{\"jobs\":[]}")})
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.State != runstate.RunRunning {
		t.Fatalf("approval barrier lost: %s, %v", run.State, err)
	}
	approver := authorityRoot("approver", "a")
	approver.Grants = []capability.Grant{capability.Grant("approvals.write")}
	authorityPersist(t, tenant.s, approver)
	_, err = tenant.TransitionNode(t.Context(), nodeTransition{Ref: gate.Ref, To: runstate.NodeSuccess, Cause: causeApprovalResolve, Actor: runstate.Actor{Kind: runstate.ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: approver.Prefix}}, Extra: transitionExtra{Approval: &runstate.ApprovalResolution{Decision: "approved"}}})
	if err != nil {
		t.Fatal(err)
	}
	run, err = tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.State != runstate.RunSuccess {
		t.Fatalf("resumed completion: %s, %v", run.State, err)
	}
}

func TestControllerReconcileRenewsOnlyContinuousCapacity(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(map[bool]string{true: "live", false: "expired"}[live], func(t *testing.T) {
			group := nodeproto.ConcurrencySpec{Group: "capacity", Scope: new("global"), Capacity: new(1)}
			tenant, request, now := controllerPlanFixture(t, nodeproto.PlanDoc{
				Concurrency: []nodeproto.ConcurrencySpec{group},
				Jobs:        []nodeproto.JobSpec{{ID: "job", Concurrency: []nodeproto.ConcurrencySpec{group}}},
			})
			controllerJobActor(t, tenant, request.RunID)
			expiry := *now
			if live {
				expiry = now.Add(time.Second)
			}
			for _, table := range []string{"concurrency_holders", "admission_work"} {
				rewriteSchemaExec(t, tenant.s.db, "UPDATE "+table+" SET lease_expires_at=? WHERE team=? AND run_id=?", expiry.UnixNano(), tenant.team, request.RunID)
			}
			if _, err := tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"concurrency_holders", "admission_work"} {
				for _, node := range []string{"", "job"} {
					var actual int64
					if err := tenant.s.db.QueryRow("SELECT lease_expires_at FROM "+table+" WHERE team=? AND run_id=? AND node_id=?", tenant.team, request.RunID, node).Scan(&actual); err != nil {
						t.Fatal(err)
					}
					want := expiry.UnixNano()
					if node == "" {
						want = now.Add(defaultConcurrencyLease).UnixNano()
					} else if live {
						want = now.Add(controllerOwnedHorizon).UnixNano()
					}
					if actual != want {
						t.Fatalf("%s node=%q continuous=%t: expiry=%d want %d", table, node, live, actual, want)
					}
				}
			}
		})
	}
}

func TestRetryPlannerFailureBeforeHandoffProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 3.3s measured in the short class; the full suite runs it")
	}
	random := rand.New(rand.NewPCG(83, 117))
	for range 12 {
		tenant, request, _ := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}, Concurrency: []nodeproto.ConcurrencySpec{{Group: "retry", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")}}})
		accepted, err := tenant.AcceptPlan(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		retry, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"}, Request: runspec.Request{Pipeline: "p", Lineage: runspec.Lineage{RetryOf: request.RunID}}})
		if err != nil {
			t.Fatal(err)
		}
		root := authorityRoot("retry-launcher", "")
		root.Subject.Kind = capability.SubjectService
		root.Grants = []capability.Grant{capability.ClaimsLaunch}
		authorityPersist(t, tenant.s, root)
		generation := int64(random.IntN(1000) + 1)
		rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET generation=? WHERE team=? AND id=?`, generation, tenant.team, retry.RunID)
		demand := UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitPlan, RunID: runspec.RunID(retry.RunID)}}
		fundUnitClaim(t, tenant.s)
		prior, stale := meteredUnitClaim(t, tenant.s, demand)
		if err := tenant.s.ReleaseUnit(t.Context(), prior, prior.Award.CapabilityPrefix); err != nil {
			t.Fatal(err)
		}
		current, actor := meteredUnitClaim(t, tenant.s, demand)
		if actor.Owner.Generation != runspec.ClaimGeneration(generation+2) {
			t.Fatalf("retry generation reset: %+v", actor.Owner)
		}
		for _, kind := range []runstate.ActorKind{runstate.ActorRunHost, runstate.ActorHolder, runstate.ActorOperator, runstate.ActorController} {
			forged := actor
			forged.Kind = kind
			if _, err := tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: retry.RunID, NodeID: "job"}, forged); err == nil {
				t.Fatalf("planner acquired copied job capacity as %s", kind)
			}
		}
		if _, err := tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: retry.RunID, Generation: int64(actor.Owner.Generation)}, actor); err != nil {
			t.Fatalf("live planner lost run capacity: %v", err)
		}
		before, err := tenant.ReadSnapshot(t.Context(), retry.RunID)
		if err != nil || before.Run.Spec == nil || before.Run.Spec.Hash != accepted.Spec.Hash {
			t.Fatalf("retry lost its carried seal: %+v, %v", before.Run, err)
		}
		for _, rejected := range []runstate.FinishRunRequest{
			{Unit: stale.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(retry.RunID), Cause: runstate.RunFinish, Actor: stale}},
		} {
			if _, err := tenant.FinishRunAndUnit(t.Context(), rejected); err == nil {
				t.Fatalf("invalid planner finished retry: %+v", rejected)
			}
		}
		for _, rejected := range []nodeTransition{
			{To: runstate.NodeReady, Cause: runstate.CauseRelease},
			{To: runstate.NodeSkipped, Cause: causeDependency},
			{To: runstate.NodeFailed, Cause: causeOwnerPolicy, Extra: transitionExtra{OwnerPolicy: &runstate.OwnerPolicyResult{Kind: runstate.OwnerPolicyKind("admission_fail")}, Result: &runstate.NodeResult{Outcome: runstate.NodeFailed}}},
			{To: runstate.NodeApprovalPending, Cause: runstate.CauseApprovalOpen},
		} {
			rejected.Ref, rejected.Actor = runspec.NodeRef{RunID: runspec.RunID(retry.RunID), NodeID: "job"}, actor
			if _, err := tenant.TransitionNode(t.Context(), rejected); err == nil {
				t.Fatalf("planner changed copied job before handoff: %+v", rejected)
			}
		}
		after, err := tenant.ReadSnapshot(t.Context(), retry.RunID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("refused finish changed retry: %v", err)
		}
		actor.Result = &capability.RecordedResult{Actor: actor.Authority, Method: "POST", Pattern: "/api/v1/runs/{run}/finish", RunID: retry.RunID, Generation: int64(actor.Generation), Digest: strings.Repeat("a", 64)}
		finish := runstate.FinishRunRequest{Unit: current.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(retry.RunID), Cause: runstate.RunFinish, Actor: actor, MaskedError: "untrusted planner text"}}
		for replay := range 2 {
			applied, err := tenant.FinishRunAndUnit(t.Context(), finish)
			if err != nil || applied.Replayed != (replay == 1) {
				t.Fatalf("retry preparation failure: %+v, %v", applied, err)
			}
		}
		after, err = tenant.ReadSnapshot(t.Context(), retry.RunID)
		if err != nil || after.Run.State != runstate.RunFailed || after.Run.Spec.Hash != accepted.Spec.Hash || after.Run.Owner == nil || after.Run.Owner.Generation != current.Owner.Generation {
			t.Fatalf("retry finish lost its seal or fence: %+v, %v", after.Run, err)
		}
		for _, node := range after.Nodes {
			if node.Status != runstate.StatusDone || node.Outcome != runstate.NodeCancelled || node.StateActor.Result != nil {
				t.Fatalf("preparation failure did not close copied job internally: %+v", node)
			}
		}
	}
}
