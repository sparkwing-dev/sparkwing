package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func clonePredicateRun(t *testing.T, s *Store, request []byte, creator, subject string) {
	t.Helper()
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,created_principal,created_subject_kind,created_at,log_retention_class,log_retention_days) SELECT team,'later',2,pipeline,status,?,'later',request_sealed_at,repo_key,repo_display,dispatch,intake_mode,?,?,2,log_retention_class,log_retention_days FROM runs WHERE id='run'`, request, creator, subject)
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT team,'later',unit_kind,2,2,expires_at,needs_labels,prefers_labels FROM triggers WHERE id='run'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT team,'later',node_id,'pending',2,spec_json,execution_json FROM nodes WHERE run_id='run'`)
	fixtureRunWriter(t, s, "later", runspec.PrincipalWriter(subject, creator))
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "later", NodeID: "job"})
	indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitTrigger, RunID: "later"})
}

func TestClaimPredicateSkipsOverflowCappedHead(t *testing.T) {
	for _, stored := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "stored"}[stored], func(t *testing.T) {
			s, demand := burstFixture(t, runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "cloud"}}})
			fundUnitClaim(t, s)
			owner := authorityMember(t, s, "owner", RoleOwner)
			var raw []byte
			if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE id='run'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			clonePredicateRun(t, s, raw, "other", "account")
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET ready_at=1 WHERE run_id='run'`)
			if stored {
				zero := int64(0)
				if err := (&Tenant{s: s, team: "a"}).SetOverflowLimit(t.Context(), owner.Authority, &zero); err != nil {
					t.Fatal(err)
				}
			} else {
				var sealed runspec.SealedRequest
				if err := json.Unmarshal(raw, &sealed); err != nil {
					t.Fatal(err)
				}
				sealed.Request.Burst.OverflowMinutes = new(int64)
				raw, _ = json.Marshal(sealed)
				rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, raw)
			}
			launcher := authorityRoot("launcher", "")
			launcher.Subject.Kind = capability.SubjectService
			launcher.Grants = []capability.Grant{capability.ClaimsLaunch}
			until := s.now().Add(time.Hour)
			launcher.ExpiresAt = &until
			authorityPersist(t, s, launcher)
			demand.CapabilityPrefix, demand.Target = launcher.Prefix, nil
			demand.Kinds = []capability.UnitKind{capability.UnitNode}
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || claim.RunID != "later" {
				t.Fatalf("capped head hid funded work: %+v %v", claim, err)
			}
		})
	}
}

func TestClaimPredicateAllowsPassedNonemptyGuards(t *testing.T) {
	s, demand := unitClaimFixture(t)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET spec_json=?,spec_hash='fixture',sealed_at=1,plan_json=?,plan_hash='fixture'`, []byte(`{"resolved":{"guards":{"passed":true,"when":["approved"]}}}`), []byte(`{}`))
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.NodeID != "job" {
		t.Fatalf("passed guard blocked claim: %+v %v", claim, err)
	}
}

func TestClaimPredicateMachineRequesterRequiresPersonalOwnership(t *testing.T) {
	s, demand := unitClaimFixture(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	_, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &owner.Authority, RegistrationID: "runner:a:shared", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "shared"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	personal := burstPersonalClaimant(t, s, owner.Authority, "desktop")
	var raw []byte
	if err := s.db.QueryRow(`SELECT request_json FROM runs WHERE id='run'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	clonePredicateRun(t, s, raw, "owner", "account")
	rewriteSchemaExec(t, s.db, `UPDATE runs SET created_principal='shared',created_subject_kind='machine' WHERE id='run'`)
	fixtureRunWriter(t, s, "run", runspec.PrincipalWriter("machine", "shared"))
	demand.CapabilityPrefix, demand.Target = personal.Prefix, nil
	demand.Kinds = []capability.UnitKind{capability.UnitNode}
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.RunID != "later" {
		t.Fatalf("shared creator hid personal owner's later run: %+v %v", claim, err)
	}
}

func TestClaimPredicateUsesCanonicalRepositoryRouting(t *testing.T) {
	for _, display := range []string{"git@github.com:acme/repo.git", "https://github.com/acme/repo.git/"} {
		t.Run(display, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			scoped := authorityRoot("scoped", "a")
			scoped.Grants = []capability.Grant{capability.NodesClaim}
			scoped.Repositories = []string{"github.com/acme/*"}
			authorityPersist(t, s, scoped)
			rewriteSchemaExec(t, s.db, `UPDATE runs SET repo_display=?`, display)
			demand.CapabilityPrefix = scoped.Prefix
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || claim.NodeID != "job" {
				t.Fatalf("valid repository spelling did not route: %+v %v", claim, err)
			}
		})
	}
}

func TestClaimPredicateSkipsDeletingTeam(t *testing.T) {
	s, demand := launcherFairnessFixture(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO team_deletions(slug,requested_at,state) VALUES ('a',1,'pending')`)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || claim.Team != "b" {
		t.Fatalf("deleting team hid healthy work: claim=%+v error=%v", claim, err)
	}
}

func TestClaimPredicatePreservesSelectorWhitespaceAndCase(t *testing.T) {
	for _, test := range []struct{ selector, label string }{{"\tgpu\u00a0", "gpu"}, {"Sparkwing-4CPU", "Sparkwing-4CPU"}} {
		t.Run(test.label, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			root := authorityRoot("metered-label-claimant", "a")
			root.RootMetered = true
			authorityPersist(t, s, root)
			fundUnitClaim(t, s)
			body, _ := json.Marshal(map[string]any{"requires": map[string]any{"value": []string{test.selector}}})
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET execution_json=?`, body)
			indexFixtureUnit(t, s, "a", UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
			demand.CapabilityPrefix = root.Prefix
			demand.Labels = []string{test.label}
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil || claim.NodeID != "job" {
				t.Fatalf("selector %q rejected actual label %q: %+v %v", test.selector, test.label, claim, err)
			}
		})
	}
}
