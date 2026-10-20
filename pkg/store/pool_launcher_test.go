package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
	seal.Request.Git.SHA = strings.Repeat("a", 40)
	seal.Request.Git.Ref = "refs/heads/main"
	seal.Request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Ref: seal.Request.Git.Ref, SHA: seal.Request.Git.SHA, DefaultBranch: "main", DefaultBranchPush: true}}
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
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	ref, fence := launcherFence(t, s, demand, claim, capability.UnitNode)
	awards, err := s.ReadLauncherAwards(t.Context(), ref, nil)
	if err != nil || len(awards) != 1 {
		t.Fatalf("pool reconciliation: %+v %v", awards, err)
	}
	c := awards[0].Capability
	if c.Delivery != capability.DeliveryBootstrap || c.Billing.Metered || c.RootMetered || c.Billing.ExecutorClass != "runner" {
		t.Fatalf("pool award must be unmetered bootstrap: %+v", c)
	}
	if prepared, err := s.RenewLauncherPreparation(t.Context(), ref, []capability.LeaseRef{fence}); err != nil || len(prepared) != 1 {
		t.Fatalf("pool preparation: %v %v", prepared, err)
	}
	allocation, err := s.CloudAwardResources(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix})
	if err != nil || allocation == nil || allocation.Cores != 4 {
		t.Fatalf("pool allocation: %+v %v", allocation, err)
	}
	issued, err := s.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	_, actor, err := renewalActor(issued.Capability)
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	ordinal := int64(0)
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: &ordinal}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenewUnit(t.Context(), issued.Capability.Prefix, true); err != nil {
		t.Fatal(err)
	}
	if chargedMicro(t, s) != 0 {
		t.Fatal("self-hosted pool charged credits")
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
			if _, err := s.ClaimUnit(t.Context(), demand); err == nil {
				t.Fatalf("pool launcher escaped its %s", other)
			}
		})
	}
}
