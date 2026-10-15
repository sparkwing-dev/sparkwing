package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func controllerPlanFixture(t *testing.T, plan nodeproto.PlanDoc) (*Tenant, PlanAcceptance, *time.Time) {
	t.Helper()
	tenant, request, now := planFixture(t, runspec.DispatchController)
	var err error
	request.Seal.Plan, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	request.Seal.Resolved.Settings, err = (fakeIntakeContracts{}).ResolvePlan(settings.PlanInputs{Request: request.Seal.Request, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	return tenant, request, now
}

func controllerJobActor(t *testing.T, tenant *Tenant, runID string) Actor {
	t.Helper()
	root := authorityRoot("runner", "a")
	root.Grants = []capability.Grant{capability.NodesClaim}
	authorityPersist(t, tenant.s, root)
	demand := UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: runID, NodeID: "job"}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}
	claim, err := tenant.s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{runID, "job"}, To: NodeRunning, Cause: CauseStart, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	return actor
}

func TestControllerReconcileResumesReportsAcrossLeaseGap(t *testing.T) {
	tenant, request, now := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}, {ID: "next", Needs: []string{"job"}}}})
	actor := controllerJobActor(t, tenant, request.RunID)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET until=? WHERE name='controller.owner'`, now.UnixNano())
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{request.RunID, "job"}, To: NodeSuccess, Cause: CauseReport, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	next, err := tenant.ReadNode(t.Context(), NodeRef{request.RunID, "next"})
	if err != nil || next.ReadyAt != nil {
		t.Fatalf("advanced without controller: %+v, %v", next, err)
	}
	lease, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "successor", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		visited, err := tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), lease, "", 10)
		if err != nil || len(visited) != 1 || visited[0].Team != tenant.team || visited[0].RunID != request.RunID {
			t.Fatalf("reconcile: %+v, %v", visited, err)
		}
	}
	next, err = tenant.ReadNode(t.Context(), NodeRef{request.RunID, "next"})
	if err != nil || next.ReadyAt == nil || next.StateSeq != 1 {
		t.Fatalf("successor release: %+v, %v", next, err)
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.State != RunRunning || run.Owner == nil || run.Owner.Holder != lease.Holder || run.Owner.Generation != lease.Generation {
		t.Fatalf("successor owner: %+v, %v", run, err)
	}
}

func TestControllerReconcilePagesAllTeamsAndFinishesAtomically(t *testing.T) {
	s, _, _ := maintenanceStore(t)
	for i, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		maintenanceRun(t, s, id, i+1, RunPending)
		rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller',sealed_at=1,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan',plan_accepted_generation=1 WHERE id=?`, []byte(`{}`), []byte(`{"jobs":[]}`), id)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET team='b' WHERE id='c'`)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='host' WHERE id='b'`)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET intake_mode='publish' WHERE id='d'`)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET sealed_at=NULL,spec_json=NULL,spec_hash='',plan_json=NULL,plan_hash='' WHERE id='e'`)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET plan_accepted_generation=0 WHERE id='f'`)
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var visited []ControllerRun
	after := ""
	for {
		page, err := s.AsOperator().ReconcileControllerRuns(t.Context(), lease, after, 1)
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
		if err != nil || run.State != RunSuccess {
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
		if err != nil || run.State != RunPending || run.StateSeq != 0 {
			t.Fatalf("ineligible run changed: %+v, %v", run, err)
		}
	}
}

func TestControllerReconcileRequiresLiveExactFenceEvenWithoutRuns(t *testing.T) {
	for _, problem := range []string{"expired", "holder", "generation", "name", "limit"} {
		t.Run(problem, func(t *testing.T) {
			s, _, now := maintenanceStore(t)
			lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "replica", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			limit := 10
			switch problem {
			case "expired":
				*now = lease.ExpiresAt
			case "holder":
				lease.Holder = "other"
			case "generation":
				lease.Generation++
			case "name":
				lease.Name = "maintenance"
			case "limit":
				limit = 0
			}
			if _, err := s.AsOperator().ReconcileControllerRuns(t.Context(), lease, "", limit); err == nil {
				t.Fatal("invalid controller pass succeeded")
			}
		})
	}
}

func TestControllerReconcileRollsBackFailedCompletion(t *testing.T) {
	s, tenant, _ := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, RunRunning)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller',sealed_at=1,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan',plan_accepted_generation=1`, []byte(`{}`), []byte(`{"jobs":[]}`))
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_completion BEFORE INSERT ON run_effects BEGIN SELECT RAISE(ABORT,'completion unavailable'); END`)
	if visited, err := s.AsOperator().ReconcileControllerRuns(t.Context(), lease, "", 10); err == nil || len(visited) != 0 {
		t.Fatalf("failed completion: %+v, %v", visited, err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != RunRunning || run.StateSeq != 0 || run.FinishedAt != nil {
		t.Fatalf("partial completion: %+v, %v", run, err)
	}
	rewriteSchemaCount(t, s.db, "events", 0)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_completion`)
	if _, err := s.AsOperator().ReconcileControllerRuns(t.Context(), lease, "", 10); err != nil {
		t.Fatal(err)
	}
	run, err = tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != RunSuccess {
		t.Fatalf("completion retry: %+v, %v", run, err)
	}
	if _, err := s.AsOperator().ReconcileControllerRuns(t.Context(), SingletonLease{Name: lease.Name, Holder: "stale", Generation: lease.Generation}, "", 10); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("stale pass: %v", err)
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
			if err != nil || run.Owner == nil || run.Owner.Kind != OwnerTrigger || run.Spec == nil || run.Spec.Hash != accepted.Spec.Hash {
				t.Fatalf("retry owner/seal: %+v, %v", run, err)
			}
			controller := Actor{Kind: ActorController, Owner: OwnerLeaseRef{Kind: OwnerController, Name: "controller.owner", Holder: "controller", Generation: 1, RunID: retry.RunID}}
			if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{retry.RunID, "job"}, To: NodeReady, Cause: CauseRelease, Actor: controller}); err == nil {
				t.Fatal("controller bypassed retry planner")
			}
			lease := SingletonLease{Name: "controller.owner", Holder: "controller", Generation: 1}
			visited, err := tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), lease, "", 10)
			if err != nil || len(visited) != 1 || visited[0].RunID != request.RunID {
				t.Fatalf("retry settled before handoff: %+v, %v", visited, err)
			}
			root := authorityRoot("launcher", "")
			root.Subject.Kind = capability.SubjectService
			root.Grants = []capability.Grant{capability.ClaimsLaunch}
			authorityPersist(t, tenant.s, root)
			demand := UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitPlan, RunID: retry.RunID}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}
			claim, err := tenant.s.ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			if started {
				issued, err := tenant.s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tenant.s.RenewUnit(t.Context(), issued.Capability.Prefix, true); err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, tenant.s.db, "UPDATE triggers SET disruptions=2 WHERE team=? AND id=?", tenant.team, retry.RunID)
			}
			*now = claim.Award.LeaseExpiresAt
			reaper, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			reaped, err := tenant.s.ReapUnits(t.Context(), reaper, 10)
			if err != nil || len(reaped) != 1 || reaped[0].RunID != retry.RunID || reaped[0].Requeued == started || reaped[0].Failed != started {
				t.Fatalf("retry planner loss: %+v, %v", reaped, err)
			}
			run, err = tenant.ReadRun(t.Context(), retry.RunID)
			want := RunPending
			if started {
				want = RunFailed
			}
			if err != nil || run.State != want || run.Spec.Hash != accepted.Spec.Hash {
				t.Fatalf("recovered retry: %+v, %v", run, err)
			}
			if started {
				return
			}
			claim, err = tenant.s.ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			issued, err := tenant.s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
			if err != nil {
				t.Fatal(err)
			}
			request.RunID = retry.RunID
			request.Actor = Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix, Owner: claim.Owner}
			resumed, err := tenant.AcceptPlan(t.Context(), request)
			if err != nil || !resumed.Replayed || resumed.Owner == nil || resumed.Owner.Kind != OwnerController {
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
	plans := tenant.s.plans.(fakePlanContracts)
	plans.resolve = func(settings.ExpansionInputs) (map[string]runspec.JobExecutionSettings, error) {
		return map[string]runspec.JobExecutionSettings{}, nil
	}
	tenant.s.plans = plans
	actor := controllerJobActor(t, tenant, request.RunID)
	before, err := tenant.ReadNode(t.Context(), NodeRef{request.RunID, "gate"})
	if err != nil || before.Status != StatusApprovalPending {
		t.Fatalf("initial approval: %+v, %v", before, err)
	}
	rewriteSchemaExec(t, tenant.s.db, "UPDATE leases SET until=? WHERE name='controller.owner'", now.UnixNano())
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{request.RunID, "job"}, To: NodeSuccess, Cause: CauseReport, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, tenant.s.db, "eval_units", 0)
	*now = now.Add(time.Minute)
	reaper, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if reaped, err := tenant.s.ReapUnits(t.Context(), reaper, 10); err != nil || len(reaped) != 0 {
		t.Fatalf("owner lapse failed waiting work: %+v, %v", reaped, err)
	}
	lease, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "successor", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), lease, "", 10); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSchemaCount(t, tenant.s.db, "eval_units", 1)
	gate, err := tenant.ReadNode(t.Context(), before.Ref)
	if err != nil || gate.Status != StatusApprovalPending || gate.StateSeq != before.StateSeq {
		t.Fatalf("approval changed during takeover: %+v, %v", gate, err)
	}
	root := authorityRoot("launcher", "")
	root.Subject.Kind = capability.SubjectService
	root.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, tenant.s, root)
	claim, err := tenant.s.ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitEval, RunID: request.RunID, NodeID: "job", SourceGeneration: 1}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := tenant.s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tenant.AcceptExpansion(t.Context(), ExpansionSubmission{Source: NodeRef{request.RunID, "job"}, SourceGeneration: 1, Actor: Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix}, Body: []byte("{\"jobs\":[]}")})
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.State != RunRunning {
		t.Fatalf("approval barrier lost: %s, %v", run.State, err)
	}
	approver := authorityRoot("approver", "a")
	approver.Grants = []capability.Grant{capability.Grant("approvals.write")}
	authorityPersist(t, tenant.s, approver)
	_, err = tenant.TransitionNode(t.Context(), NodeTransition{Ref: gate.Ref, To: NodeSuccess, Cause: CauseApprovalResolve, Actor: Actor{Kind: ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: approver.Prefix}}, Extra: TransitionExtra{Approval: &ApprovalResolution{Decision: "approved"}}})
	if err != nil {
		t.Fatal(err)
	}
	run, err = tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.State != RunSuccess {
		t.Fatalf("resumed completion: %s, %v", run.State, err)
	}
}

func TestControllerReconcileRenewsOnlyContinuousCapacity(t *testing.T) {
	tenant, request, now := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}})
	rewriteSchemaExec(t, tenant.s.db, "INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES ('a','live',2,1),('a','expired',2,1)")
	for _, id := range []string{"live", "expired"} {
		expiry := now.Add(time.Minute)
		if id == "expired" {
			expiry = *now
		}
		rewriteSchemaExec(t, tenant.s.db, "INSERT INTO concurrency_holders(team,key,holder_id,run_id,node_id,claimed_at,lease_expires_at,generation) VALUES ('a',?,?,?,'',?,?,1)", id, id, request.RunID, now.Add(-time.Minute).UnixNano(), expiry.UnixNano())
		rewriteSchemaExec(t, tenant.s.db, "INSERT INTO concurrency_waiters(team,key,holder_id,run_id,node_id,arrived_at,lease_expires_at,generation,policy) VALUES ('a',?,?,?,'job',?,?,1,'queue')", id, id, request.RunID, now.Add(-time.Minute).UnixNano(), expiry.UnixNano())
	}
	lease, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "controller", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), lease, "", 10); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
		for _, id := range []string{"live", "expired"} {
			var expires int64
			if err := tenant.s.db.QueryRow("SELECT lease_expires_at FROM "+table+" WHERE team='a' AND key=? AND holder_id=?", id, id).Scan(&expires); err != nil {
				t.Fatal(err)
			}
			want := lease.ExpiresAt.UnixNano()
			if id == "expired" {
				want = now.UnixNano()
			}
			if expires != want {
				t.Fatalf("%s %s expiry: %d, want %d", table, id, expires, want)
			}
		}
	}
}
