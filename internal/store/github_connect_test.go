package store

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
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
	if err != nil || out.ConnectedBy != a.Principal.Capability.Subject.ID || out.Team != tenant.Team() || !out.CreatedAt.Equal(*now) {
		t.Fatalf("binding = %+v: %v", out, err)
	}
	account, err := s.AsOperator().ResolveSignIn(t.Context(), SignInProfile{Provider: ProviderGoogle, Subject: "99", Email: "foreign@example.test", EmailVerified: true}, SignUpConditions{}, *now)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','99',?,'foreign@example.test',1,1)`, account.Account.ID)
	raw, _, _, err := s.AsOperator().CreateAccountSession(t.Context(), account.Account, account.Account.ActiveTeam, time.Hour)
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

func githubConnectionFixture(t *testing.T) (*Store, *Tenant, capability.Authorized, *time.Time) {
	t.Helper()
	s, tenant, a, now := githubMutationFixture(t)
	rewriteSchemaExec(t, s.db, `INSERT INTO identities(provider,subject,account_id,created_at) VALUES ('github','501','owner',1)`)
	return s, tenant, a, now
}

func TestGitHubConnectionRequiresSameLiveOwnerSessionProperty(t *testing.T) {
	property := func(mode uint8, suffix uint32) bool {
		s, tenant, a, now := githubConnectionFixture(t)
		defer s.Close()
		if subject, err := tenant.LinkedGitHubSubject(t.Context(), a); err != nil || subject != "501" {
			t.Fatalf("live owner subject=%q: %v", subject, err)
		}
		switch mode % 13 {
		case 0:
			a.Target.Team = "b"
		case 1:
			rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE team='a'`)
		case 2:
			rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='editor' WHERE team='a'`)
		case 3:
			rewriteSchemaExec(t, s.db, `DELETE FROM sessions WHERE hash='owner'`)
		case 4:
			a.Access.Grants = []capability.Grant{capability.RunsRead}
		case 5:
			a.Target.RunID = "foreign"
		case 6:
			a.Principal.Capability.Subject.ID = "foreign"
		case 7:
			a.ResultReplay = true
		case 8:
			child := authorityRoot("delegated", "a")
			child.Parent = &a.Principal.Authority
			child.Subject = a.Principal.Capability.Subject
			child.Grants = []capability.Grant{capability.TeamAdmin}
			authorityPersist(t, s, child)
			a.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}
		case 9:
			a.Principal.Class = capability.CredentialBearer
		case 10:
			*now = now.AddDate(100, 0, 0)
		case 11:
			a.Principal.Capability.Team = "b"
		case 12:
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET team='b' WHERE hash='owner'`)
		}
		before := purgeRows(t, s, "a", true)
		subject, read := tenant.LinkedGitHubSubject(t.Context(), a)
		consume := tenant.ConsumeGitHubConnect(t.Context(), a, "nonce", now.Add(time.Duration(suffix)+time.Second))
		out, bind := tenant.BindAuthorizedGitHubInstallation(t.Context(), a, GitHubAppInstallation{InstallationID: 9, AccountID: 70, AccountLogin: "acme", AccountType: "Organization", GitHubUserID: 501})
		var nonces int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM flow_nonces`).Scan(&nonces); err != nil {
			t.Fatal(err)
		}
		return read != nil && consume != nil && bind != nil && subject == "" && out == (GitHubAppInstallation{}) && nonces == 0 && reflect.DeepEqual(before, purgeRows(t, s, "a", true))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 52}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubConnectionCommitsIdentityActorAndStoreClock(t *testing.T) {
	s, tenant, a, now := githubConnectionFixture(t)
	in := GitHubAppInstallation{InstallationID: 8, AccountID: 70, AccountLogin: "acme", AccountType: "Organization", GitHubUserID: 501}
	first, err := tenant.BindAuthorizedGitHubInstallation(t.Context(), a, in)
	if err != nil || first.ConnectedBy != "owner" || first.Team != "a" || !first.CreatedAt.Equal(*now) || !first.UpdatedAt.Equal(*now) {
		t.Fatalf("binding=%+v: %v", first, err)
	}
	*now = now.Add(time.Second)
	in.AccountLogin = "renamed"
	next, err := tenant.BindAuthorizedGitHubInstallation(t.Context(), a, in)
	if err != nil || !next.CreatedAt.Equal(first.CreatedAt) || !next.UpdatedAt.Equal(*now) || next.AccountLogin != "renamed" {
		t.Fatalf("refreshed=%+v: %v", next, err)
	}
	for _, mode := range []string{"identity", "actor", "team", "provider", "type", "foreign-owner", "write-failure"} {
		t.Run(mode, func(t *testing.T) {
			candidate := in
			switch mode {
			case "identity":
				candidate.GitHubUserID++
			case "actor":
				candidate.ConnectedBy = "forged"
			case "team":
				candidate.Team = "b"
			case "provider":
				candidate.InstallationID = 0
			case "type":
				candidate.AccountType = "forged"
			case "foreign-owner":
				other := &Tenant{s: s, team: "b"}
				candidate.InstallationID = 99
				if _, err := other.TestOnlySeedGitHubInstallation(t.Context(), candidate, *now); err != nil {
					t.Fatal(err)
				}
			case "write-failure":
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_binding BEFORE INSERT ON github_app_installations BEGIN SELECT RAISE(ABORT,'refused'); END`)
				t.Cleanup(func() { rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_binding`) })
			}
			before := purgeRows(t, s, "a", true)
			out, err := tenant.BindAuthorizedGitHubInstallation(t.Context(), a, candidate)
			if err == nil || out != (GitHubAppInstallation{}) || !reflect.DeepEqual(before, purgeRows(t, s, "a", true)) {
				t.Fatalf("refusal=%+v: %v", out, err)
			}
			if mode == "foreign-owner" && !errors.Is(err, ErrInstallationBoundElsewhere) {
				t.Fatalf("foreign owner error=%v", err)
			}
		})
	}
	rewriteSchemaExec(t, s.db, `DELETE FROM identities WHERE provider='github'`)
	if subject, err := tenant.LinkedGitHubSubject(t.Context(), a); !errors.Is(err, ErrNotFound) || subject != "" {
		t.Fatalf("unlinked subject=%q: %v", subject, err)
	}
	if out, err := tenant.BindAuthorizedGitHubInstallation(t.Context(), a, in); err == nil || out != (GitHubAppInstallation{}) {
		t.Fatalf("unlinked binding=%+v: %v", out, err)
	}
}

func TestGitHubConnectionNonceIsAtomicAndExpiresOnStoreClock(t *testing.T) {
	s, tenant, a, now := githubConnectionFixture(t)
	expires := now.Add(time.Nanosecond)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() { results <- tenant.ConsumeGitHubConnect(t.Context(), a, "one-use", expires) })
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("nonce had %d winners", winners)
	}
	var stored int64
	if err := s.db.QueryRow(`SELECT expires_at FROM flow_nonces WHERE kind='github_app_connect' AND nonce='one-use'`).Scan(&stored); err != nil || stored != expires.UnixNano() {
		t.Fatalf("expiry=%d: %v", stored, err)
	}
	*now = expires
	if err := tenant.ConsumeGitHubConnect(t.Context(), a, "expired", expires); err == nil {
		t.Fatal("nonce accepted at expiry")
	}
	if err := tenant.ConsumeGitHubConnect(t.Context(), a, "", now.Add(time.Hour)); err == nil {
		t.Fatal("empty nonce accepted")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM flow_nonces`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("refused nonce committed count=%d: %v", count, err)
	}
}
