package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func poolLauncherFixture(t *testing.T) (*Store, UnitDemand) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	owner := authorityMember(t, s, "pool-admin", RoleOwner)
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, Authority: &owner.Authority, RegistrationID: "runner:a:deploy-launcher", Team: "a",
		Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "deploy-launcher"}, Grants: []capability.Grant{capability.ClaimsLaunch},
		Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, ReservedPools: []string{"deploy"}, Trust: capability.TrustVerifiedDefault,
	})
	if err != nil {
		t.Fatal(err)
	}
	demand.CapabilityPrefix = issued.Capability.Prefix
	var body []byte
	if err := s.db.QueryRowContext(t.Context(), `SELECT request_json FROM runs WHERE id='run'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var seal runspec.SealedRequest
	if err := json.Unmarshal(body, &seal); err != nil {
		t.Fatal(err)
	}
	seal.Provenance, seal.Request.Git = referenceAppProvenance(t, "refs/heads/main", fixtureOtherSHA)
	body, err = json.Marshal(seal)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, body)
	rewriteSchemaExec(t, s.db, `INSERT INTO pool_access_rules(team,id,pool,repo_key,created_by,created_at) VALUES ('a','deploy','deploy','gh:1','owner',1)`)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=CAST(json_set(execution_json, '$.requires', json(?)) AS BLOB) WHERE run_id='run'`, `{"set":true,"value":["pool=deploy","team-4cpu"]}`)
	return s, demand
}

func TestPoolLauncherUsesBootstrapAndNeverMeters(t *testing.T) {
	s, demand := poolLauncherFixture(t)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	ref, fence := launcherMeteredFence(t, s, demand, claim, capability.UnitNode)
	awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), ref, nil, nil, nil, nil, nil)
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
	if err != nil || allocation == nil || allocation.Cores != 2 {
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
	admitExecution(t, tenant, actor)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: (*runspec.AttemptOrdinal)(&ordinal)}); err != nil {
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
				rewriteSchemaExec(t, s.db, `INSERT INTO secrets(team,name,value,principal,created_at,updated_at,masked,repo,pipeline,shared,only_on) VALUES ('a','DEPLOY','ciphertext','account',1,1,1,'','',0,'deploy')`)
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

type poolPin struct {
	tenant   *Tenant
	input    IntakeRequest
	admitted AdmissionResult
	root     capability.Capability
	lease    SingletonLease
}

func (f poolPin) claim(t *testing.T, kind capability.UnitKind, run string) (capability.Capability, error) {
	t.Helper()
	demand := UnitDemand{CapabilityPrefix: f.root.Prefix, Kinds: []capability.UnitKind{kind}}
	if run != "" {
		demand.Target = &UnitRef{Kind: kind, RunID: runspec.RunID(run)}
		if kind == capability.UnitNode {
			demand.Target.NodeID = "job"
		}
	}
	award, err := f.tenant.s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		return capability.Capability{}, err
	}
	if run != "" && string(award.RunID) != run || award.Kind != kind {
		t.Fatalf("pool award: %+v", award)
	}
	issued, err := f.tenant.s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: award.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Capability, nil
}

func poolPinFixture(t *testing.T, secrets ...string) poolPin {
	t.Helper()
	return poolPinFixtureOn(t, []string{"deploy"}, secrets...)
}

func poolPinFixtureOn(t *testing.T, reserved []string, secrets ...string) poolPin {
	t.Helper()
	ctx := t.Context()
	tenant, input, now := intakeFixture(t)
	s := tenant.s
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
	for _, pool := range reserved {
		rewriteSchemaExec(t, s.db, `INSERT INTO pool_access_rules(team,id,pool,repo_key,created_by,created_at) VALUES ('a',?,?,'gh:1','owner',1)`, pool, pool)
	}
	admitted, err := tenant.AdmitRun(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Request.Request.Dispatch != runspec.DispatchController {
		t.Fatal("pool pin must offer a launcher plan unit")
	}
	seal := admitted.Request
	root := authorityRoot("deploy-launcher", "a")
	root.Grants, root.ReservedPools, root.Trust = []capability.Grant{capability.ClaimsLaunch}, reserved, capability.TrustVerifiedDefault
	authorityPersist(t, s, root)
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "controller.owner", "controller", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f := poolPin{tenant: tenant, input: input, admitted: admitted, root: root, lease: lease}
	planner, err := f.claim(t, capability.UnitPlan, "")
	if err != nil {
		t.Fatal(err)
	}
	_, actor, err := renewalActor(planner)
	if err != nil {
		t.Fatal(err)
	}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}}
	raw, err := json.Marshal(testBodyPlan(plan))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range secrets {
		rewriteSchemaExec(t, s.db, `INSERT INTO secrets(team,name,value,principal,created_at,updated_at,masked,repo,pipeline,shared,only_on) VALUES ('a',?,'ciphertext','account',1,1,0,'gh:1','p',0,'deploy')`, name)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: seal, Plan: plan, Declarations: declarations})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(ctx, PlanAcceptance{RunID: admitted.RunID, Actor: actor, Seal: runspec.SealInput{Request: seal, Plan: raw, Declarations: declarations, Resolved: runspec.Resolved{Args: seal.Request.RequestedArgs, SecretArgs: []string{}, SecretNames: secrets, DefaultBranch: runspec.Optional[string]{Set: true, Value: "main"}, DefaultBranchSource: runspec.DefaultBranchApp, Settings: resolved, Guards: runspec.Guards{Passed: true}}}}); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRunLevelPoolPinCompletesThroughLauncher(t *testing.T) {
	ctx := t.Context()
	f := poolPinFixture(t)
	tenant, admitted, s := f.tenant, f.admitted, f.tenant.s
	worker, err := f.claim(t, capability.UnitNode, "")
	if err != nil {
		t.Fatal(err)
	}
	_, actor, err := renewalActor(worker)
	if err != nil {
		t.Fatal(err)
	}
	admitExecution(t, tenant, actor)
	for i, state := range []runstate.NodeState{runstate.NodeRunning, runstate.NodeSuccess} {
		cause := runstate.CauseStart
		if i > 0 {
			cause = runstate.CauseReport
		}
		if _, err := tenant.TransitionNode(ctx, nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(admitted.RunID), NodeID: "job"}, To: state, Cause: cause, Actor: actor, SourceAttempt: sourceAttempt(int64(i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AsOperator().ReconcileControllerRuns(ctx, f.lease, "", 10); err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(ctx, admitted.RunID)
	if err != nil || run.State != runstate.RunSuccess {
		t.Fatalf("pool-pinned run: %+v %v", run, err)
	}
	rewriteSchemaCount(t, s.db, "billable_intervals", 0)
}

// An access rule an admin removes stops every later use of the pool: secret
// release and OIDC for work already on it, and placement, including of retries
// that copy an older run's sealed identity.
func TestRemovedPoolRuleCutsOffRetriesOfVerifiedRuns(t *testing.T) {
	ctx := t.Context()
	f := poolPinFixture(t, "DEPLOY")
	s := f.tenant.s
	retry := func(key string) string {
		t.Helper()
		result, err := f.tenant.AdmitRun(ctx, IntakeRequest{Authority: f.input.Authority, IdempotencyKey: key, Request: runspec.Request{Pipeline: "p", Lineage: runspec.Lineage{RetryOf: f.admitted.RunID}}})
		if err != nil {
			t.Fatal(err)
		}
		return result.RunID
	}
	running := f.admitted.RunID
	worker, err := f.claim(t, capability.UnitNode, running)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: worker.Prefix})
	if err != nil {
		t.Fatal(err)
	}
	secret := capability.Target{Team: "a", SecretName: "DEPLOY"}
	if err := s.AsOperator().CheckBinding(ctx, principal, capability.BindDeclaredSecret, secret); err != nil {
		t.Fatalf("allowed pool withheld its secret: %v", err)
	}
	oidc := capability.Authorized{Principal: principal, Target: capability.Target{Team: "a", RunID: running}}
	if _, err := f.tenant.ReadOIDCFacts(ctx, oidc); err != nil {
		t.Fatalf("allowed pool withheld OIDC: %v", err)
	}
	rewriteSchemaExec(t, s.db, `DELETE FROM pool_access_rules WHERE team='a' AND id='deploy'`)
	if err := s.AsOperator().CheckBinding(ctx, principal, capability.BindDeclaredSecret, secret); err == nil {
		t.Fatal("a removed pool rule still released the reserved secret")
	}
	if _, err := f.tenant.ReadOIDCFacts(ctx, oidc); err == nil {
		t.Fatal("a removed pool rule still minted OIDC on the pool")
	}
	late := retry("after-removal")
	if _, err := f.claim(t, capability.UnitPlan, late); err == nil {
		t.Fatal("a retry placed on a pool whose rule was removed")
	}
}

// A launcher holding another still-allowed pool must not keep a job's pool
// privileges once the rule for the pool that job requires is removed.
func TestRemovedPoolRuleIsNotBypassedThroughAnotherPool(t *testing.T) {
	ctx := t.Context()
	f := poolPinFixtureOn(t, []string{"deploy", "staging"}, "DEPLOY")
	s := f.tenant.s
	worker, err := f.claim(t, capability.UnitNode, f.admitted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: worker.Prefix})
	if err != nil {
		t.Fatal(err)
	}
	oidc := capability.Authorized{Principal: principal, Target: capability.Target{Team: "a", RunID: f.admitted.RunID}}
	if _, err := f.tenant.ReadOIDCFacts(ctx, oidc); err != nil {
		t.Fatalf("allowed pool withheld OIDC: %v", err)
	}
	rewriteSchemaExec(t, s.db, `DELETE FROM pool_access_rules WHERE team='a' AND pool='deploy'`)
	if _, err := f.tenant.ReadOIDCFacts(ctx, oidc); err == nil {
		t.Fatal("OIDC minted through a pool the job does not require")
	}
	if err := s.AsOperator().CheckBinding(ctx, principal, capability.BindDeclaredSecret, capability.Target{Team: "a", SecretName: "DEPLOY"}); err == nil {
		t.Fatal("a removed pool rule still released the reserved secret")
	}
	late, err := f.tenant.AdmitRun(ctx, IntakeRequest{Authority: f.input.Authority, IdempotencyKey: "late", Request: runspec.Request{Pipeline: "p", Lineage: runspec.Lineage{RetryOf: f.admitted.RunID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.claim(t, capability.UnitPlan, late.RunID); err == nil {
		t.Fatal("a retry placed on a pool whose rule was removed")
	}
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
				rewriteSchemaExec(t, s.db, `UPDATE pool_access_rules SET pool='other'`)
			}
			if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err == nil {
				t.Fatalf("pool launcher escaped its %s", other)
			}
		})
	}
}

// A runner that reports running in a Kubernetes pod is persistent Kubernetes
// capacity, which never takes verified default-branch work; a credential not
// enrolled as Kubernetes would hide that, so its claims are refused with the fix.
func TestKubernetesRunnerNeedsAKubernetesEnrollment(t *testing.T) {
	for _, platform := range []string{"", "kubernetes"} {
		t.Run("platform="+platform, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			owner := authorityMember(t, s, "owner", RoleOwner)
			issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
				Kind: capability.ProvisionMachine, Authority: &owner.Authority, RegistrationID: "runner:a:pod", Team: "a",
				Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "pod"}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim},
				Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, Platform: platform,
			})
			if err != nil {
				t.Fatal(err)
			}
			demand.CapabilityPrefix, demand.Kubernetes = issued.Capability.Prefix, true
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if platform == "" {
				if err == nil || !strings.Contains(err.Error(), "--platform kubernetes") {
					t.Fatalf("unenrolled pod claim: %+v %v", claim, err)
				}
				return
			}
			if err != nil || claim.RunID != "run" || claim.Award.CapabilityPrefix == "" {
				t.Fatalf("enrolled pod claim: %+v %v", claim, err)
			}
		})
	}
}
