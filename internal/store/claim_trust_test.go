package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// Reserved secrets require launcher enrollment; trust stays on the root credential.
func TestReservedSecretPoolTakesOnlyVerifiedDefaultBranchWork(t *testing.T) {
	for _, tc := range []struct {
		name     string
		secret   bool
		trust    capability.Trust
		verified bool
		claims   bool
		launcher bool
	}{
		{name: "pool without a reserved secret takes any run", claims: true},
		{name: "reserved secret pool refuses an unverified run", secret: true},
		{name: "reserved secret pool launcher refuses an unverified run", secret: true, launcher: true},
		{name: "reserved secret pool launcher takes the verified default branch", secret: true, verified: true, launcher: true, claims: true},
		{name: "reserved secret pool refuses a persistent runner even for verified work", secret: true, verified: true},
		{name: "explicit verified pool refuses a persistent runner", trust: capability.TrustVerifiedDefault, verified: true},
		{name: "any trust still refuses a persistent secret pool runner", secret: true, trust: capability.TrustAny},
		{name: "operator opts a pool launcher into any run", secret: true, trust: capability.TrustAny, claims: true, launcher: true},
		{name: "declared verified-default refuses an unverified run", trust: capability.TrustVerifiedDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, named := range []bool{false, true} {
				checkPoolTrust(t, named, tc.secret, tc.trust, tc.verified, tc.claims, tc.launcher)
			}
		})
	}
}

func checkPoolTrust(t *testing.T, named, secret bool, trust capability.Trust, verified, claims, launcher bool) {
	t.Helper()
	ctx := t.Context()
	s := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies), fixturePolicies(store.Policies{ClaimProfiles: &matchingEnrollment{profile: match.Profile{}}}))
	tenant, plan, err := storePlanSealRequestWith(ctx, s, func(_ *store.IntakeRequest, doc *nodeproto.PlanDoc) {
		doc.Jobs = []nodeproto.JobSpec{{ID: "job", Requires: []string{"pool=prod"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionNode(ctx, store.ReadyNode{Ref: runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "job"}, Actor: plan.Actor}); err != nil {
		t.Fatal(err)
	}
	if secret {
		if _, err := s.DB().ExecContext(ctx, `INSERT INTO secrets(team,name,value,principal,created_at,updated_at,masked,pipeline,shared,only_on) VALUES (?,'DEPLOY','ciphertext','account',1,1,1,'',0,'prod')`, tenant.Team()); err != nil {
			t.Fatal(err)
		}
	}
	branch := "feature"
	if verified {
		branch = "main"
	}
	if err := grantPool(ctx, s, tenant.Team(), plan.RunID, "prod", branch); err != nil {
		t.Fatal(err)
	}
	authority := plan.Seal.Request.AuthorityRef
	grants := []capability.Grant{capability.NodesClaim}
	if launcher {
		grants = []capability.Grant{capability.ClaimsLaunch}
	}
	issued, err := s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tenant.Team()), RegistrationID: "runner:" + string(tenant.Team()) + ":pool", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "pool"}, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, ReservedPools: []string{"prod"}, Trust: trust})
	if err != nil {
		t.Fatal(err)
	}
	demand := store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}}
	if named {
		demand.Target = &store.UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(plan.RunID), NodeID: "job"}
	}
	claim, err := s.AsOperator().ClaimUnit(ctx, demand)
	if !claims {
		assertMatchingRefusal(t, err, named)
		return
	}
	if err != nil || claim.RunID != plan.RunID {
		t.Fatalf("pool claim: %+v %v", claim, err)
	}
}

func TestRunnerTrustIsAnAdminSetCredentialFact(t *testing.T) {
	ctx := t.Context()
	s := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies), fixturePolicies(store.Policies{ClaimProfiles: &matchingEnrollment{profile: match.Profile{}}}))
	_, plan, err := storePlanSealRequestWith(ctx, s, func(*store.IntakeRequest, *nodeproto.PlanDoc) {})
	if err != nil {
		t.Fatal(err)
	}
	authority := plan.Seal.Request.AuthorityRef
	spec := func(id string, personal bool) capability.ProvisionSpec {
		return capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: personal, Authority: &authority, Team: plan.Seal.Request.Request.Team, RegistrationID: "runner:" + plan.Seal.Request.Request.Team + ":" + id, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: id}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, Trust: capability.TrustAny}
	}
	if _, err := s.AsOperator().ProvisionCapability(ctx, spec("personal", true)); err == nil {
		t.Fatal("a personal machine registered a trust class")
	}
	issued, err := s.AsOperator().ProvisionCapability(ctx, spec("shared", false))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix})
	if err != nil || p.Capability.Trust != capability.TrustAny {
		t.Fatalf("stored trust = %q, %v", p.Capability.Trust, err)
	}
}

func grantPool(ctx context.Context, s *store.Store, team store.Team, run, pool, branch string) error {
	var body []byte
	if err := s.DB().QueryRowContext(ctx, `SELECT request_json FROM runs WHERE team=? AND id=?`, team, run).Scan(&body); err != nil {
		return err
	}
	var seal runspec.SealedRequest
	if err := json.Unmarshal(body, &seal); err != nil {
		return err
	}
	var err error
	seal.Provenance, err = runspec.AppProvenance(runspec.AppFacts{Repository: "acme/repo", Event: "push", RepositoryID: 1, DeliveryKey: "pool-delivery", VerifiedAt: time.Now(), Ref: "refs/heads/" + branch, SHA: seal.Request.Git.SHA, DefaultBranch: "main", DefaultBranchPush: branch == "main"}, runspec.Optional[runspec.PullRequest]{}, seal.Request.Pipeline)
	if err != nil {
		return err
	}
	if body, err = json.Marshal(seal); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE runs SET request_json=? WHERE team=? AND id=?`, body, team, run); err != nil {
		return err
	}
	_, err = s.DB().ExecContext(ctx, `INSERT INTO pool_access_rules(team,id,pool,repo_key,created_by,created_at) VALUES (?,?,?,'gh:1','owner',1)`, team, pool, pool)
	return err
}
