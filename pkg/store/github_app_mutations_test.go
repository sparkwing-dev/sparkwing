package store

import (
	"errors"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func githubMutationFixture(t *testing.T) (*Store, *Tenant, capability.Authorized, *time.Time) {
	t.Helper()
	s := authorityStore(t)
	actor := authorityMember(t, s, "owner", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	now := time.Unix(1800000000, 123456789).UTC()
	s.clock = func() time.Time { return now }
	if _, err := tenant.TestOnlySeedGitHubInstallation(t.Context(), GitHubAppInstallation{InstallationID: 7, AccountID: 1, AccountLogin: "acme", AccountType: "Organization"}, now); err != nil {
		t.Fatal(err)
	}
	a := teamAuthorization(t, s, actor, "a", "PUT", "/api/v1/team/github-app/triggers")
	return s, tenant, a, &now
}

func TestGitHubAppMutationsRequireCurrentAdministrationProperty(t *testing.T) {
	property := func(mode uint8, delegated bool) bool {
		s, tenant, a, _ := githubMutationFixture(t)
		defer s.Close()
		if delegated {
			child := authorityRoot("delegated", "a")
			child.Parent = &a.Principal.Authority
			child.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}
			child.Grants = []capability.Grant{capability.TeamAdmin}
			authorityPersist(t, s, child)
			a.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}
		}
		row := GitHubAppTrigger{RepositoryID: 701, Repository: "acme/app", InstallationID: 7, Pipeline: "ci", Push: true}
		if _, err := tenant.PutAuthorizedGitHubTrigger(t.Context(), a, row); err != nil {
			t.Fatal(err)
		}
		if err := tenant.PutAuthorizedGitHubAutomation(t.Context(), a, GitHubAppAutomation{RepositoryID: 701, Repository: "acme/app", InstallationID: 7}); err != nil {
			t.Fatal(err)
		}
		switch mode % 8 {
		case 0:
			a.Target.Team = "b"
		case 1:
			rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE team='a'`)
		case 2:
			rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='reader' WHERE team='a'`)
		case 3:
			rewriteSchemaExec(t, s.db, `DELETE FROM sessions WHERE hash='owner'`)
		case 4:
			a.Access.Grants = []capability.Grant{capability.RunsRead}
		case 5:
			a.Target.RunID = "run"
		case 6:
			a.Principal.Authority.ID = "foreign"
		case 7:
			a.ResultReplay = true
		}
		before := purgeRows(t, s, "a", true)
		out, put := tenant.PutAuthorizedGitHubTrigger(t.Context(), a, row)
		auto := tenant.PutAuthorizedGitHubAutomation(t.Context(), a, GitHubAppAutomation{RepositoryID: 701, Repository: "acme/app", InstallationID: 7})
		extras, extra := tenant.SetAuthorizedGitHubExtraRepos(t.Context(), a, "acme/app", []string{"acme/lib"}, map[string]int64{"acme/lib": 42})
		del := tenant.DeleteAuthorizedGitHubTrigger(t.Context(), a, 701, "ci")
		revoke := tenant.DeleteAuthorizedGitHubAutomation(t.Context(), a, 701)
		unbind := tenant.UnbindAuthorizedGitHubInstallation(t.Context(), a, 7)
		return put != nil && auto != nil && extra != nil && del != nil && revoke != nil && unbind != nil && reflect.DeepEqual(out, GitHubAppTrigger{}) && extras == nil && reflect.DeepEqual(before, purgeRows(t, s, "a", true))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 40}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubAppMutationsCommitActorTimeAndConsentGeneration(t *testing.T) {
	s, tenant, a, now := githubMutationFixture(t)
	ctx := t.Context()
	row, err := tenant.PutAuthorizedGitHubTrigger(ctx, a, GitHubAppTrigger{RepositoryID: 701, Repository: "Acme/App", InstallationID: 7, Pipeline: "ci", Push: true})
	if err != nil || row.CreatedBy != "owner" || !row.CreatedAt.Equal(*now) {
		t.Fatalf("subscription=%+v: %v", row, err)
	}
	grant := GitHubAppAutomation{RepositoryID: 701, Repository: "acme/app", InstallationID: 7}
	if err := tenant.PutAuthorizedGitHubAutomation(ctx, a, grant); err != nil {
		t.Fatal(err)
	}
	first, err := tenant.GitHubAppAutomation(ctx, 7, 701)
	if err != nil || first.EnabledBy != "owner" || !first.EnabledAt.Equal(*now) {
		t.Fatalf("consent=%+v: %v", first, err)
	}
	*now = now.Add(time.Second)
	if err := tenant.PutAuthorizedGitHubAutomation(ctx, a, grant); err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.GitHubAppAutomation(ctx, 7, 701)
	if err != nil || !replay.EnabledAt.Equal(first.EnabledAt) {
		t.Fatalf("replay=%+v: %v", replay, err)
	}
	if err := tenant.DeleteAuthorizedGitHubAutomation(ctx, a, 701); err != nil {
		t.Fatal(err)
	}
	if err := tenant.PutAuthorizedGitHubAutomation(ctx, a, grant); err != nil {
		t.Fatal(err)
	}
	replaced, err := tenant.GitHubAppAutomation(ctx, 7, 701)
	if err != nil || !replaced.EnabledAt.Equal(*now) {
		t.Fatalf("replacement=%+v: %v", replaced, err)
	}
	if err := s.inTx(ctx, func(tx *storeTx) error { return tenant.checkGitHubAppAutomationTx(ctx, tx, first) }); !errors.Is(err, ErrGitHubAppAutomationRevoked) {
		t.Fatalf("old consent=%v", err)
	}
	list, err := tenant.SetAuthorizedGitHubExtraRepos(ctx, a, "Acme/App", []string{"acme/lib"}, map[string]int64{"Acme/Lib": 42})
	if err != nil || !reflect.DeepEqual(list, []string{"acme/lib"}) {
		t.Fatalf("extras=%v: %v", list, err)
	}
	var actor string
	var id, at int64
	if err := s.db.QueryRowContext(ctx, `SELECT extra_repo_id,created_by,created_at FROM github_app_extra_repos WHERE team='a'`).Scan(&id, &actor, &at); err != nil || id != 42 || actor != "owner" || at != now.UnixNano() {
		t.Fatalf("extra=%d,%s,%d: %v", id, actor, at, err)
	}
	if err := tenant.UnbindAuthorizedGitHubInstallation(ctx, a, 7); err != nil {
		t.Fatal(err)
	}
	if rows, err := tenant.GitHubAppTriggers(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("unbound subscriptions=%+v: %v", rows, err)
	}
	if _, err := tenant.SetAuthorizedGitHubExtraRepos(ctx, a, "acme/app", nil, nil); err != nil {
		t.Fatalf("clear after unbind: %v", err)
	}
	if rows, err := tenant.GitHubAppAutomations(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("unbound consents=%+v: %v", rows, err)
	}
}

func TestGitHubAppMutationRefusalsRollBack(t *testing.T) {
	for _, mode := range []string{"missing-id", "foreign-owner", "foreign-installation", "suspended", "actor", "final-authority", "insert-failure"} {
		t.Run(mode, func(t *testing.T) {
			s, tenant, a, _ := githubMutationFixture(t)
			ctx := t.Context()
			if _, err := tenant.SetAuthorizedGitHubExtraRepos(ctx, a, "acme/app", []string{"acme/old"}, map[string]int64{"acme/old": 1}); err != nil {
				t.Fatal(err)
			}
			row := GitHubAppTrigger{RepositoryID: 701, Repository: "acme/app", InstallationID: 7, Pipeline: "ci", Push: true}
			ids := map[string]int64{"acme/new": 2}
			extra := "acme/new"
			switch mode {
			case "missing-id":
				ids = nil
			case "foreign-owner":
				extra = "evil/repo"
			case "foreign-installation":
				row.InstallationID = 8
			case "suspended":
				rewriteSchemaExec(t, s.db, `UPDATE github_app_installations SET suspended=1`)
			case "actor":
				row.CreatedBy = "forged"
			case "final-authority":
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER remove_admin AFTER INSERT ON github_app_extra_repos BEGIN DELETE FROM memberships WHERE team='a'; END`)
			case "insert-failure":
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER fail_extra BEFORE INSERT ON github_app_extra_repos BEGIN SELECT RAISE(ABORT,'fixture failure'); END`)
			}
			before := purgeRows(t, s, "a", true)
			var err error
			if mode == "foreign-installation" || mode == "actor" {
				var out GitHubAppTrigger
				out, err = tenant.PutAuthorizedGitHubTrigger(ctx, a, row)
				if !reflect.DeepEqual(out, GitHubAppTrigger{}) {
					t.Fatal("failed write exposed result")
				}
			} else {
				var out []string
				out, err = tenant.SetAuthorizedGitHubExtraRepos(ctx, a, "acme/app", []string{extra}, ids)
				if out != nil {
					t.Fatal("failed write exposed result")
				}
			}
			if err == nil || !reflect.DeepEqual(before, purgeRows(t, s, "a", true)) {
				t.Fatalf("mutation escaped rollback: %v", err)
			}
		})
	}
}
