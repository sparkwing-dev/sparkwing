package store

import (
	"fmt"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func githubConnectFixture(t *testing.T) (*Store, *Tenant, capability.Authorized, *time.Time) {
	t.Helper()
	s, session, _, now := accountMutationFixture(t)
	team := Team(session.Principal.Capability.Team)
	tenant, err := s.ForTeam(t.Context(), team)
	if err != nil {
		t.Fatal(err)
	}
	a := teamAuthorization(t, s, Actor{Authority: session.Principal.Authority}, team, "PUT", "/api/v1/team/github-app/extra-repos")
	rewriteSchemaExec(t, s.db, `INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','42',?,'member@example.test',1,1)`, a.Principal.Capability.Subject.ID)
	return s, tenant, a, now
}

func TestGitHubConnectRechecksSessionAndIdentityProperty(t *testing.T) {
	property := func(choice uint8, suffix uint32) bool {
		s, tenant, a, now := githubConnectFixture(t)
		id := a.Principal.Capability.Subject.ID
		switch choice % 13 {
		case 0:
			a.Target.Team = fmt.Sprint("foreign-", suffix)
		case 1:
			rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='reader' WHERE account_id=?`, id)
		case 2:
			rewriteSchemaExec(t, s.db, `DELETE FROM sessions WHERE hash=?`, a.Principal.Authority.ID)
		case 3:
			*now = now.Add(2 * time.Hour)
		case 4:
			a.Principal.Class = capability.CredentialBearer
		case 5:
			a.Principal.Capability.Subject.ID = fmt.Sprint("foreign-", suffix)
		case 6:
			a.Principal.Capability.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "parent"}
		case 7:
			rewriteSchemaExec(t, s.db, `DELETE FROM identities WHERE provider='github' AND account_id=?`, id)
		case 8:
			a.Access.Grants = []capability.Grant{capability.RunsRead}
		case 9:
			a.Target.RunID = fmt.Sprint("run-", suffix)
		case 10:
			a.ResultReplay = true
		case 11:
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=? WHERE hash=?`, now.UnixNano(), a.Principal.Authority.ID)
		case 12:
			parent := authorityRoot(fmt.Sprint("parent-", suffix), a.Principal.Capability.Team)
			parent.Subject, parent.Grants = a.Principal.Capability.Subject, a.Principal.Capability.Grants
			authorityPersist(t, s, parent)
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET capability_prefix=? WHERE hash=?`, parent.Prefix, a.Principal.Authority.ID)
		}
		subject, read := tenant.LinkedGitHubSubject(t.Context(), a)
		consume := tenant.ConsumeGitHubConnect(t.Context(), a, fmt.Sprint("nonce-", suffix), now.Add(time.Hour))
		row, bind := tenant.BindAuthorizedGitHubInstallation(t.Context(), a, GitHubAppInstallation{InstallationID: 7, AccountID: 42, AccountLogin: "member", AccountType: "User", GitHubUserID: 42})
		var nonces, installations int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM flow_nonces`).Scan(&nonces); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM github_app_installations`).Scan(&installations); err != nil {
			t.Fatal(err)
		}
		return read != nil && subject == "" && consume != nil && bind != nil && reflect.DeepEqual(row, GitHubAppInstallation{}) && nonces == 0 && installations == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubConnectRechecksBindingAndStoreClock(t *testing.T) {
	s, tenant, a, now := githubConnectFixture(t)
	if err := tenant.ConsumeGitHubConnect(t.Context(), a, "expired", *now); err == nil {
		t.Fatal("expired nonce accepted")
	}
	expires := now.Add(time.Hour)
	if err := tenant.ConsumeGitHubConnect(t.Context(), a, "once", expires); err != nil {
		t.Fatal(err)
	}
	if err := tenant.ConsumeGitHubConnect(t.Context(), a, "once", expires); err == nil {
		t.Fatal("replayed nonce accepted")
	}
	row := GitHubAppInstallation{InstallationID: 7, AccountID: 42, AccountLogin: "member", AccountType: "User", GitHubUserID: 42}
	for _, change := range []func(*GitHubAppInstallation){
		func(r *GitHubAppInstallation) { r.Team = "foreign" },
		func(r *GitHubAppInstallation) { r.ConnectedBy = "foreign" },
		func(r *GitHubAppInstallation) { r.GitHubUserID = 99 },
		func(r *GitHubAppInstallation) { r.AccountType = "invalid" },
	} {
		bad := row
		change(&bad)
		if _, err := tenant.BindAuthorizedGitHubInstallation(t.Context(), a, bad); err == nil {
			t.Fatalf("forged binding accepted: %+v", bad)
		}
	}
	out, err := tenant.BindAuthorizedGitHubInstallation(t.Context(), a, row)
	if err != nil || out.ConnectedBy != a.Principal.Capability.Subject.ID || out.Team != tenant.Team() || !out.CreatedAt.Equal(now.Truncate(time.Second)) {
		t.Fatalf("binding = %+v: %v", out, err)
	}
	account, err := s.ResolveSignIn(t.Context(), SignInProfile{Provider: ProviderGoogle, Subject: "99", Email: "foreign@example.test", EmailVerified: true}, SignUpConditions{}, *now)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','99',?,'foreign@example.test',1,1)`, account.Account.ID)
	raw, _, _, err := s.CreateAccountSession(t.Context(), account.Account, account.Account.ActiveTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.ForTeam(t.Context(), account.Account.ActiveTeam)
	if err != nil {
		t.Fatal(err)
	}
	foreign := teamAuthorization(t, s, Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(raw)}}, other.Team(), "PUT", "/api/v1/team/github-app/extra-repos")
	row.GitHubUserID = 99
	if _, err := other.BindAuthorizedGitHubInstallation(t.Context(), foreign, row); err != ErrInstallationBoundElsewhere {
		t.Fatalf("foreign takeover = %v", err)
	}
}
