package store_test

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A pool runner runs one job after another in shared host state, so a pool that a secret
// is reserved to takes only verified default-branch work unless an admin registered it
// with another trust class. The class lives on the runner's root credential.
func TestReservedSecretPoolTakesOnlyVerifiedDefaultBranchWork(t *testing.T) {
	for _, tc := range []struct {
		name     string
		secret   bool
		trust    capability.Trust
		verified bool
		claims   bool
	}{
		{name: "pool without a reserved secret takes any run", claims: true},
		{name: "reserved secret pool refuses an unverified run", secret: true},
		{name: "reserved secret pool takes the verified default branch", secret: true, verified: true, claims: true},
		{name: "operator opts the pool into any run", secret: true, trust: capability.TrustAny, claims: true},
		{name: "declared verified-default refuses an unverified run", trust: capability.TrustVerifiedDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, named := range []bool{false, true} {
				checkPoolTrust(t, named, tc.secret, tc.trust, tc.verified, tc.claims)
			}
		})
	}
}

func checkPoolTrust(t *testing.T, named, secret bool, trust capability.Trust, verified, claims bool) {
	t.Helper()
	ctx := t.Context()
	s := storetest.OpenSQLite(t)
	tenant, plan, err := storePlanSealRequestWith(ctx, s, func(_ *store.IntakeRequest, doc *nodeproto.PlanDoc) {
		doc.Jobs = []nodeproto.JobSpec{{ID: "job", Requires: []string{"pool=prod"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionNode(ctx, store.NodeTransition{Ref: store.NodeRef{RunID: plan.RunID, NodeID: "job"}, Actor: plan.Actor, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE runs SET allowed_pools=? WHERE team=? AND id=?`, []byte(`["prod"]`), tenant.Team(), plan.RunID); err != nil {
		t.Fatal(err)
	}
	if secret {
		if _, err := s.DB().ExecContext(ctx, `INSERT INTO secrets(team,name,value,principal,created_at,updated_at,masked,pipeline,shared,only_on) VALUES (?,'DEPLOY','ciphertext','account',1,1,1,'',0,'prod')`, tenant.Team()); err != nil {
			t.Fatal(err)
		}
	}
	if verified {
		var body []byte
		if err := s.DB().QueryRowContext(ctx, `SELECT request_json FROM runs WHERE team=? AND id=?`, tenant.Team(), plan.RunID).Scan(&body); err != nil {
			t.Fatal(err)
		}
		var seal runspec.SealedRequest
		if err := json.Unmarshal(body, &seal); err != nil {
			t.Fatal(err)
		}
		git := &seal.Request.Git
		git.Ref = "refs/heads/main"
		git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Ref: git.Ref, SHA: git.SHA, DefaultBranch: "main", DefaultBranchPush: true}}
		if body, err = json.Marshal(seal); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().ExecContext(ctx, `UPDATE runs SET request_json=? WHERE team=? AND id=?`, body, tenant.Team(), plan.RunID); err != nil {
			t.Fatal(err)
		}
	}
	authority := plan.Seal.Request.AuthorityRef
	issued, err := s.ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(tenant.Team()), RegistrationID: "runner:" + string(tenant.Team()) + ":pool", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "pool"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, ReservedPools: []string{"prod"}, Trust: trust})
	if err != nil {
		t.Fatal(err)
	}
	s.SetClaimProfileResolver(&matchingEnrollment{profile: match.Profile{Capacity: match.Resources{Cores: 8}}})
	demand := store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}, Available: match.Resources{Cores: 8}}
	if named {
		demand.Target = &store.UnitRef{Kind: capability.UnitNode, RunID: plan.RunID, NodeID: "job"}
	}
	claim, err := s.ClaimUnit(ctx, demand)
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
	s := storetest.OpenSQLite(t)
	_, plan, err := storePlanSealRequestWith(ctx, s, func(*store.IntakeRequest, *nodeproto.PlanDoc) {})
	if err != nil {
		t.Fatal(err)
	}
	authority := plan.Seal.Request.AuthorityRef
	spec := func(id string, personal bool) capability.ProvisionSpec {
		return capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: personal, Authority: &authority, Team: plan.Seal.Request.Request.Team, RegistrationID: "runner:" + plan.Seal.Request.Request.Team + ":" + id, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: id}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}, Trust: capability.TrustAny}
	}
	if _, err := s.ProvisionCapability(ctx, spec("personal", true)); err == nil {
		t.Fatal("a personal machine registered a trust class")
	}
	issued, err := s.ProvisionCapability(ctx, spec("shared", false))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix})
	if err != nil || p.Capability.Trust != capability.TrustAny {
		t.Fatalf("stored trust = %q, %v", p.Capability.Trust, err)
	}
}
