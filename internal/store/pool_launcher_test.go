package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func poolLauncherFixture(t *testing.T) (*Store, UnitDemand) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	root := authorityRoot("deploy-launcher", "a")
	root.Grants = []capability.Grant{capability.ClaimsLaunch}
	root.ReservedPools = []string{"deploy"}
	root.Trust = capability.TrustVerifiedDefault
	authorityPersist(t, s, root)
	demand.CapabilityPrefix = root.Prefix
	var body []byte
	if err := s.db.QueryRowContext(t.Context(), `SELECT request_json FROM runs WHERE id='run'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var seal runspec.SealedRequest
	if err := json.Unmarshal(body, &seal); err != nil {
		t.Fatal(err)
	}
	seal.Provenance, seal.Request.Git = referenceAppProvenance(t, "refs/heads/main", fixtureOtherSHA)
	body, err := json.Marshal(seal)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?,allowed_pools=? WHERE id='run'`, body, []byte(`["deploy"]`))
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=json_set(execution_json, '$.requires', json(?)) WHERE run_id='run'`, `{"set":true,"value":["pool=deploy"]}`)
	return s, demand
}

func TestPoolLauncherUsesBootstrapAndNeverMeters(t *testing.T) {
	s, demand := poolLauncherFixture(t)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	ref, fence := launcherFence(t, s, demand, claim, capability.UnitNode)
	awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, nil, nil, nil)
	if err != nil || len(awards) != 1 {
		t.Fatalf("pool reconciliation: %+v %v", awards, err)
	}
	c := awards[0].Capability
	if c.Delivery != capability.DeliveryBootstrap || c.Billing.Metered || c.RootMetered || c.Billing.ExecutorClass != "runner" {
		t.Fatalf("pool award must be unmetered bootstrap: %+v", c)
	}
	if prepared, err := s.renewLauncherPreparation(t.Context(), ref, []capability.LeaseRef{fence}); err != nil || len(prepared) != 1 {
		t.Fatalf("pool preparation: %v %v", prepared, err)
	}
	allocation, err := s.AsOperator().CloudAwardResources(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix})
	if err != nil || allocation == nil || allocation.Cores != 4 {
		t.Fatalf("pool allocation: %+v %v", allocation, err)
	}
	issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	_, actor, err := renewalActor(issued.Capability)
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	ordinal := int64(0)
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: &ordinal}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().RenewUnit(t.Context(), issued.Capability.Prefix, true); err != nil {
		t.Fatal(err)
	}
	if chargedMicro(t, s) != 0 {
		t.Fatal("self-hosted pool charged credits")
	}
	rewriteSchemaCount(t, s.db, "billable_intervals", 0)
}

func TestVerifiedPoolRefusesPersistentRunnerCredentials(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		for _, kind := range []capability.UnitKind{capability.UnitNode, capability.UnitTrigger} {
			s, demand := poolLauncherFixture(t)
			root := authorityRoot("persistent-pool", "a")
			root.Grants = []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
			root.ReservedPools = []string{"deploy"}
			root.Trust = capability.TrustVerifiedDefault
			if inherited {
				root.Trust = ""
				rewriteSchemaExec(t, s.db, `INSERT INTO secrets(team,name,value,principal,created_at,updated_at,masked,pipeline,shared,only_on) VALUES ('a','DEPLOY','ciphertext','account',1,1,1,'',0,'deploy')`)
			}
			authorityPersist(t, s, root)
			demand.CapabilityPrefix = root.Prefix
			demand.Target = &UnitRef{Kind: kind, RunID: "run", NodeID: "job"}
			if kind == capability.UnitTrigger {
				demand.Target.NodeID = ""
			}
			if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err == nil {
				t.Fatalf("persistent runner claimed verified pool work: kind=%s inherited=%v", kind, inherited)
			}
		}
	}
}

func TestRunLevelPoolPinCompletesThroughLauncher(t *testing.T) {
	ctx := t.Context()
	tenant, input, now := intakeFixture(t)
	s := tenant.s
	s.intake = fakeIntakeContracts{resolve: settings.ResolvePlan}
	input.Request.Dispatch = ""
	input.Request.Options.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"pool=deploy"}}}
	input.Request.Git.Repository.Display = "https://github.com/acme/repo"
	input.VerifiedDeliveryID = "delivery"
	git := input.Request.Git
	git.Ref = "refs/heads/main"
	git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Event: "push", RepositoryID: 1, DeliveryKey: "delivery", Ref: git.Ref, SHA: git.SHA, DefaultBranch: "main", DefaultBranchPush: true, VerifiedAt: *now}}
	s.intakePolicy = fakeIntakePolicy(func(context.Context, capability.AuthorityResolver, IntakeRequest) (IntakePolicyFacts, error) {
		return IntakePolicyFacts{SourceLocation: &input.Request.Source.Location, LogRetentionClass: "r30", LogRetentionDays: 30, Delivery: &VerifiedRunDelivery{Git: git, Actor: runspec.Actor{Kind: "github", ID: "actor"}}}, nil
	})
	rewriteSchemaExec(t, s.db, `INSERT INTO pool_access_rules(team,id,pool,repo_key,created_by,created_at) VALUES ('a','deploy','deploy','gh:1','owner',1)`)
	admitted, err := tenant.AdmitRun(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Request.Request.Dispatch != runspec.DispatchController {
		t.Fatal("pool pin must offer a launcher plan unit")
	}
	seal := admitted.Request
	root := authorityRoot("deploy-launcher", "a")
	root.Grants, root.ReservedPools, root.Trust = []capability.Grant{capability.ClaimsLaunch}, []string{"deploy"}, capability.TrustVerifiedDefault
	authorityPersist(t, s, root)
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "controller.owner", "controller", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(kind capability.UnitKind) capability.Capability {
		t.Helper()
		demand := UnitDemand{CapabilityPrefix: root.Prefix, Kinds: []capability.UnitKind{kind}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}
		award, err := s.AsOperator().ClaimUnit(ctx, demand)
		if err != nil {
			t.Fatal(err)
		}
		if award.RunID != admitted.RunID || award.Kind != kind {
			t.Fatalf("pool award: %+v", award)
		}
		issued, err := s.AsOperator().ExchangeCapability(ctx, capability.ExchangeSpec{BootstrapPrefix: award.Award.CapabilityPrefix})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Capability
	}
	planner := claim(capability.UnitPlan)
	_, actor, err := renewalActor(planner)
	if err != nil {
		t.Fatal(err)
	}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: seal, Plan: plan, Declarations: declarations})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(ctx, PlanAcceptance{RunID: admitted.RunID, Actor: actor, Seal: runspec.SealInput{Request: seal, Plan: raw, Declarations: declarations, Resolved: runspec.Resolved{Args: seal.Request.RequestedArgs, SecretArgs: []string{}, DefaultBranch: runspec.Optional[string]{Set: true, Value: "main"}, DefaultBranchSource: runspec.DefaultBranchApp, Settings: resolved, Guards: runspec.Guards{Passed: true}}}}); err != nil {
		t.Fatal(err)
	}
	worker := claim(capability.UnitNode)
	_, actor, err = renewalActor(worker)
	if err != nil {
		t.Fatal(err)
	}
	for i, state := range []NodeState{NodeRunning, NodeSuccess} {
		cause := CauseStart
		if i > 0 {
			cause = CauseReport
		}
		if _, err := tenant.TransitionNode(ctx, NodeTransition{Ref: controllerwire.NodeRef{RunID: admitted.RunID, NodeID: "job"}, To: state, Cause: cause, Actor: actor, SourceAttempt: sourceAttempt(int64(i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AsOperator().ReconcileControllerRuns(ctx, lease, "", 10); err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(ctx, admitted.RunID)
	if err != nil || run.State != RunSuccess {
		t.Fatalf("pool-pinned run: %+v %v", run, err)
	}
	rewriteSchemaCount(t, s.db, "billable_intervals", 0)
}

func TestPoolLauncherCannotCrossItsScope(t *testing.T) {
	for _, other := range []string{"team", "pool", "repository"} {
		t.Run(other, func(t *testing.T) {
			s, demand := poolLauncherFixture(t)
			if other == "team" {
				root := authorityRoot("other-team-launcher", "b")
				root.Grants = []capability.Grant{capability.ClaimsLaunch}
				root.ReservedPools = []string{"deploy"}
				root.Trust = capability.TrustAny
				authorityPersist(t, s, root)
				demand.CapabilityPrefix = root.Prefix
			} else if other == "repository" {
				root := authorityRoot("other-repo-launcher", "a")
				root.Grants = []capability.Grant{capability.ClaimsLaunch}
				root.ReservedPools = []string{"deploy"}
				root.Repositories = []string{"github.com/else/*"}
				root.Trust = capability.TrustAny
				authorityPersist(t, s, root)
				demand.CapabilityPrefix = root.Prefix
			} else {
				rewriteSchemaExec(t, s.db, `UPDATE runs SET allowed_pools=? WHERE id='run'`, []byte(`["other"]`))
			}
			if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err == nil {
				t.Fatalf("pool launcher escaped its %s", other)
			}
		})
	}
}
