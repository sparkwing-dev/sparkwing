package store

import (
	"fmt"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func poolAdminFixture(t *testing.T) (*Store, *Tenant, runstate.Actor) {
	t.Helper()
	s := authorityStore(t)
	actor := authorityMember(t, s, "owner", RoleOwner)
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (1,'a',1,'acme','Organization',1,1),(2,'b',2,'other','Organization',1,1)`)
	return s, &Tenant{s: s, team: "a"}, actor
}

func TestPoolRulesCommitCanonicalSelectorsAndReplay(t *testing.T) {
	s, tenant, actor := poolAdminFixture(t)
	runner := authorityRoot("reserved", "a")
	runner.ReservedPools = []string{"prod"}
	authorityPersist(t, s, runner)
	rule := PoolAccessRule{Pool: "prod", RepositoryKey: "gh:42", Branch: "main", Source: "github_app", Route: "prod", InstallationID: 1}
	for i := range 2 {
		replay, err := tenant.WritePoolAccess(t.Context(), actor, "stable", []PoolAccessRule{rule})
		if err != nil || replay != (i > 0) {
			t.Fatalf("replayed=%v: %v", replay, err)
		}
	}
	changed := rule
	changed.Branch = "other"
	if _, err := tenant.WritePoolAccess(t.Context(), actor, "stable", []PoolAccessRule{changed}); err == nil {
		t.Fatal("changed request replayed")
	}
	rules, runners, err := tenant.ReadPoolAccess(t.Context())
	if err != nil || len(rules) != 1 || rules[0].RepositoryKey != "gh:42" || len(runners["prod"]) != 1 {
		t.Fatalf("rules=%v runners=%v: %v", rules, runners, err)
	}
	if deleted, err := tenant.DeletePoolAccess(t.Context(), actor, "foreign", "stable"); err != nil || deleted {
		t.Fatalf("foreign deletion=%v: %v", deleted, err)
	}
	if deleted, err := tenant.DeletePoolAccess(t.Context(), actor, "prod", "stable"); err != nil || !deleted {
		t.Fatalf("deletion=%v: %v", deleted, err)
	}
}

func TestPoolRuleAuthorityCannotEscapeCurrentTeamProperty(t *testing.T) {
	s, tenant, actor := poolAdminFixture(t)
	property := func(value uint16) bool {
		_, err := tenant.WritePoolAccess(t.Context(), actor, fmt.Sprint(value), []PoolAccessRule{{Pool: "prod", RepositoryKey: "gh:42", InstallationID: 2}})
		return err != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='owner'`)
	if _, err := tenant.WritePoolAccess(t.Context(), actor, "role", []PoolAccessRule{{Pool: "prod", RepositoryKey: "gh:42", InstallationID: 1}}); err == nil {
		t.Fatal("downgraded admin wrote a pool rule")
	}
	rewriteSchemaCount(t, s.db, "pool_access_rules", 0)
}

func TestRunnerDeletionRevokesOnlyTheSelectedTeamAndIdentity(t *testing.T) {
	s, tenant, actor := poolAdminFixture(t)
	for _, scope := range []struct{ prefix, team, name string }{{"a-root", "a", "runner"}, {"b-root", "b", "runner"}, {"other-root", "a", "other"}} {
		root := authorityRoot(scope.prefix, scope.team)
		root.Subject.ID = scope.name
		authorityPersist(t, s, root)
	}
	deleted, err := tenant.DeleteRunner(t.Context(), actor, "runner")
	if err != nil || deleted == nil || deleted.MachineID != "runner" {
		t.Fatalf("deleted=%v: %v", deleted, err)
	}
	for _, prefix := range []string{"b-root", "other-root"} {
		if _, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "a-root"}); err == nil {
		t.Fatal("deleted runner authenticates")
	}
	rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='owner'`)
	if _, err := tenant.DeleteRunner(t.Context(), actor, "other"); err == nil {
		t.Fatal("downgraded admin revoked a runner")
	}
}
