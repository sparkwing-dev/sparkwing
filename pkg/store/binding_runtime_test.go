package store

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestBindingUsesPersistedAuthorityAndTenantRelations(t *testing.T) {
	s, _ := unitClaimFixture(t)
	claim, err := s.ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: "runner", Target: &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	target := capability.Target{Team: "a", RunID: "run", NodeID: "job"}
	for _, rule := range []capability.BindingRule{capability.BindRun, capability.BindRunOfTrigger, capability.BindPlanOwner} {
		if err := s.CheckBinding(t.Context(), principal, rule, target); err != nil {
			t.Fatalf("%s: %v", rule, err)
		}
	}
	for _, problem := range []string{"foreign_team", "missing_job", "forged_binding", "forged_grants", "revoked"} {
		t.Run(problem, func(t *testing.T) {
			p, selected, rule := principal, target, capability.BindRunOfTrigger
			switch problem {
			case "foreign_team":
				selected.Team = "b"
			case "missing_job":
				selected.NodeID = "missing"
			case "forged_binding":
				p.Capability.Binding.NodeID = "job"
				p.Capability.Binding.Kind = capability.BindingNode
				rule = capability.BindOwnNode
			case "forged_grants":
				p.Capability.Grants = []capability.Grant{capability.ClaimsLaunch}
				rule = capability.BindClaimSelection
				selected = capability.Target{ClaimKinds: []capability.UnitKind{capability.UnitPlan}}
			case "revoked":
				rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, principal.Authority.ID)
			}
			if err := s.CheckBinding(t.Context(), p, rule, selected); err == nil {
				t.Fatal("uncommitted authority widened a binding")
			}
		})
	}
}

func TestBindingSecretRequiresTheSealedDeclarationAndGrantedPool(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchHost)
	request.Seal.Resolved.SecretNames = []string{"TOKEN"}
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO secrets(team,name,pipeline,value,principal,masked,created_at,updated_at) VALUES ('a','TOKEN','p','encrypted','owner',0,1,1)`)
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	p, err := tenant.s.ResolveAuthority(t.Context(), request.Actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TOKEN", "UNDECLARED"} {
		err := tenant.s.CheckBinding(t.Context(), p, capability.BindDeclaredSecret, capability.Target{Team: "a", SecretName: name})
		if (err == nil) != (name == "TOKEN") {
			t.Fatalf("secret %q: %v", name, err)
		}
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE secrets SET only_on='trusted' WHERE team='a' AND name='TOKEN'`)
	if err := tenant.s.CheckBinding(t.Context(), p, capability.BindDeclaredSecret, capability.Target{Team: "a", SecretName: "TOKEN"}); err == nil {
		t.Fatal("declaration granted a restricted secret without its awarded pool")
	}
}

func TestBindingNamedSelectionAndPipelineUseStoredRun(t *testing.T) {
	s, demand := unitClaimFixture(t)
	root, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckBinding(t.Context(), root, capability.BindClaimSelection, capability.Target{Team: "a", RunID: "run", NodeID: "job", ClaimKinds: []capability.UnitKind{capability.UnitNode}}); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	for _, pipeline := range []string{"ci", "unrelated", ""} {
		err := s.CheckBinding(t.Context(), p, capability.BindPipelineOfBoundRun, capability.Target{Team: "a", Pipeline: pipeline})
		if (err == nil) != (pipeline == "ci") {
			t.Fatalf("pipeline %q: %v", pipeline, err)
		}
	}
	if err := s.CheckBinding(t.Context(), root, capability.BindPipelineOfBoundRun, capability.Target{Team: "a", Pipeline: "ci"}); err == nil {
		t.Fatal("bare machine read claim sizing")
	}
}

func TestBindingDeclaredReadsDoNotGrantTheWholeRun(t *testing.T) {
	s, demand := unitClaimFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_json=? WHERE node_id='job'`, []byte(`{"id":"job","deps":["producer"]}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,outcome,generation,finished_at,spec_json,execution_json) VALUES ('a','run','producer','done','success',3,1,?,?),('a','run','sibling','done','success',2,1,?,?)`, []byte(`{"id":"producer"}`), []byte(`{}`), []byte(`{"id":"sibling"}`), []byte(`{}`))
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"producer", "sibling", "missing"} {
		err := s.CheckBinding(t.Context(), p, capability.BindReadSet, capability.Target{Team: "a", RunID: "run", NodeID: node})
		if (err == nil) != (node == "producer") {
			t.Fatalf("read %q: %v", node, err)
		}
	}
	if err := s.CheckBinding(t.Context(), p, capability.BindReadSet, capability.Target{Team: "a", RunID: "run", NodeID: "producer", SourceGeneration: 4}); err == nil {
		t.Fatal("source generation was ignored")
	}
}

func TestBindingSourceRequiresTheLiveUnstartedAward(t *testing.T) {
	for _, problem := range []string{"none", "started", "cancelled", "stale", "expired"} {
		t.Run(problem, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			claim, err := s.ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
			if err != nil {
				t.Fatal(err)
			}
			switch problem {
			case "started":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET started_at=1 WHERE node_id='job'`)
			case "cancelled":
				rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1`)
			case "stale":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+1 WHERE node_id='job'`)
			case "expired":
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=1 WHERE node_id='job'`)
			}
			err = s.CheckBinding(t.Context(), p, capability.BindSource, capability.Target{Team: "a", RunID: "run"})
			if (err == nil) != (problem == "none") {
				t.Fatalf("source %s: %v", problem, err)
			}
		})
	}
}

func TestBindingChildRequiresItsCreatingJobGeneration(t *testing.T) {
	s, demand := unitClaimFixture(t)
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO child_invocations(team,parent_run_id,parent_node_id,generation,ordinal,request_digest,child_run_id,created_at) VALUES ('a','run','job',1,1,'digest','child',1)`)
	for _, child := range []string{"child", "unrelated", ""} {
		err := s.CheckBinding(t.Context(), p, capability.BindChildOfBoundNode, capability.Target{Team: "a", RunID: "run", ChildRunID: child})
		if (err == nil) != (child == "child") {
			t.Fatalf("child %q: %v", child, err)
		}
	}
	rewriteSchemaExec(t, s.db, `UPDATE child_invocations SET generation=2`)
	if err := s.CheckBinding(t.Context(), p, capability.BindChildOfBoundNode, capability.Target{Team: "a", RunID: "run", ChildRunID: "child"}); err == nil {
		t.Fatal("child from another generation was readable")
	}
}

func TestBindingClaimKindsShareStoreGrantValidation(t *testing.T) {
	s, demand := unitClaimFixture(t)
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	for _, kinds := range [][]capability.UnitKind{{capability.UnitTrigger, capability.UnitNode}, nil, {capability.UnitNode, capability.UnitNode}, {"unknown"}, {capability.UnitNode, capability.UnitEval}} {
		err := s.CheckBinding(t.Context(), p, capability.BindClaimSelection, capability.Target{Team: "a", ClaimKinds: kinds})
		if len(kinds) == 2 && kinds[0] == capability.UnitTrigger {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatalf("accepted kinds %v", kinds)
		}
	}
}

func TestBindingUnknownRuleAndNodehostCleanupRefuse(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("root", "a")
	authorityPersist(t, s, root)
	p := capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}}
	for _, rule := range []capability.BindingRule{"unknown", capability.BindOwnCleanup} {
		var failure *apierr.Error
		if err := s.CheckBinding(t.Context(), p, rule, capability.Target{Team: "a"}); !errors.As(err, &failure) || failure.Code != apierr.Forbidden {
			t.Fatalf("%s: %v", rule, err)
		}
	}
}

func TestBindingTokenAndConsentRequireTheAccountOwner(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleEditor)
	other := authorityMember(t, s, "other", RoleEditor)
	for _, prefix := range []string{"mine", "theirs"} {
		authorityPersist(t, s, authorityRoot(prefix, "a"))
	}
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET created_by='owner' WHERE prefix='mine'`)
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET created_by='other' WHERE prefix='theirs'`)
	p, err := s.ResolveAuthority(t.Context(), owner.Authority)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []capability.BindingRule{capability.BindOwnToken, capability.BindMachineOwner} {
		for _, prefix := range []string{"mine", "theirs", "missing"} {
			err := s.CheckBinding(t.Context(), p, rule, capability.Target{Team: "a", TokenPrefix: prefix})
			if (err == nil) != (prefix == "mine") {
				t.Fatalf("%s %s: %v", rule, prefix, err)
			}
		}
	}
	otherPrincipal, err := s.ResolveAuthority(t.Context(), other.Authority)
	if err != nil {
		t.Fatal(err)
	}
	otherPrincipal.Capability.Grants = []capability.Grant{capability.TeamAdmin}
	if err := s.CheckBinding(t.Context(), otherPrincipal, capability.BindMachineOwner, capability.Target{Team: "a", TokenPrefix: "mine"}); err == nil {
		t.Fatal("team administration replaced machine-owner consent")
	}
}
