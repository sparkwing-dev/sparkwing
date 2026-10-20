package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func accountMutationFixture(t *testing.T) (*Store, capability.Authorized, string, *time.Time) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Unix(1800000000, 123).UTC()
	s.clock = func() time.Time { return now }
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.SetSessionKeyCipher(cipher)
	result, err := s.ResolveSignIn(t.Context(), SignInProfile{Provider: ProviderGoogle, Subject: "member", Email: "member@example.test", EmailVerified: true}, SignUpConditions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _, err := s.CreateAccountSession(t.Context(), result.Account, result.Account.ActiveTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(raw)})
	if err != nil {
		t.Fatal(err)
	}
	route, _ := capability.LookupRoute("POST", "/api/v1/me/active-team")
	a, err := capability.Authorize(t.Context(), s, route, capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, capability.Target{AccountID: result.Account.ID})
	if err != nil {
		t.Fatal(err)
	}
	return s, a, raw, &now
}

func accountMutationRows(t *testing.T, s *Store) []string {
	t.Helper()
	var teams int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM teams`).Scan(&teams); err != nil {
		t.Fatal(err)
	}
	return append(purgeRows(t, s, "", false), fmt.Sprint("teams=", teams))
}

func TestAccountMutationsRequireTheSameLiveDirectSessionProperty(t *testing.T) {
	property := func(choice uint8, suffix uint32) bool {
		s, a, _, now := accountMutationFixture(t)
		id := a.Principal.Capability.Subject.ID
		switch choice % 10 {
		case 0:
			a.Principal.Class = capability.CredentialBearer
		case 1:
			a.Principal.Capability.Subject.ID = fmt.Sprint("other-", suffix)
		case 2:
			a.Principal.Capability.Team = fmt.Sprint("other-", suffix)
		case 3:
			a.Target.AccountID = fmt.Sprint("other-", suffix)
		case 4:
			a.Access.Credential = capability.CredentialAny
		case 5:
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=? WHERE hash=?`, now.UnixNano(), a.Principal.Authority.ID)
		case 6:
			rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE account_id=?`, id)
		case 7:
			*now = now.Add(2 * time.Hour)
		case 8:
			a.Principal.Capability.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "parent"}
		case 9:
			parent := authorityRoot(fmt.Sprint("parent-", suffix), a.Principal.Capability.Team)
			parent.Subject = a.Principal.Capability.Subject
			parent.Grants = a.Principal.Capability.Grants
			authorityPersist(t, s, parent)
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET capability_prefix=? WHERE hash=?`, parent.Prefix, a.Principal.Authority.ID)
		}
		before := accountMutationRows(t, s)
		selectErr := s.SelectAccountTeam(t.Context(), a, Team(a.Principal.Capability.Team))
		_, createErr := s.CreateAccountTeam(t.Context(), a, TeamInfo{Slug: "created"})
		_, acceptErr := s.AcceptAccountInvitation(t.Context(), a, "missing")
		_, unlinkErr := s.UnlinkAccountIdentity(t.Context(), a, ProviderGoogle)
		return selectErr != nil && createErr != nil && acceptErr != nil && unlinkErr != nil && reflect.DeepEqual(before, accountMutationRows(t, s))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 36}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountMutationsCommitSessionAndAccountTogether(t *testing.T) {
	s, a, raw, now := accountMutationFixture(t)
	created, err := s.CreateAccountTeam(t.Context(), a, TeamInfo{Slug: " SECOND ", DisplayName: "  Second  "})
	if err != nil || created.Slug != "second" || created.CreatedBy != a.Principal.Capability.Subject.ID || created.DisplayName != "Second" {
		t.Fatalf("create=%+v: %v", created, err)
	}
	session, err := s.LookupSession(t.Context(), raw)
	if err != nil || session.Team != created.Slug {
		t.Fatalf("selected session=%+v: %v", session, err)
	}
	if err := s.SelectAccountTeam(t.Context(), a, created.Slug); err == nil {
		t.Fatal("stale session selection committed")
	}
	a.Principal, err = s.ResolveAuthority(t.Context(), a.Principal.Authority)
	if err != nil {
		t.Fatal(err)
	}
	account, err := s.Account(t.Context(), created.CreatedBy)
	if err != nil || account.ActiveTeam != created.Slug {
		t.Fatalf("selected account=%+v: %v", account, err)
	}
	before := accountMutationRows(t, s)
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_account_session BEFORE UPDATE OF team ON sessions BEGIN SELECT RAISE(ABORT,'session refused'); END`)
	if _, err := s.CreateAccountTeam(t.Context(), a, TeamInfo{Slug: "rolled-back"}); err == nil || !reflect.DeepEqual(before, accountMutationRows(t, s)) {
		t.Fatalf("creation escaped session rollback: %v", err)
	}
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_account_session`)
	*now = now.Add(time.Minute)
	if err := s.SelectAccountTeam(t.Context(), a, created.Slug); err != nil {
		t.Fatal(err)
	}
}

func TestAccountUnlinkKeepsSelectedSessionAndRequiresRecentSignIn(t *testing.T) {
	s, a, raw, now := accountMutationFixture(t)
	id := a.Principal.Capability.Subject.ID
	if _, err := s.LinkIdentity(t.Context(), id, SignInProfile{Provider: ProviderGitHub, Subject: "linked", Email: "other@example.test"}, *now); err != nil {
		t.Fatal(err)
	}
	other, _, _, err := s.CreateAccountSession(t.Context(), Account{ID: id}, Team(a.Principal.Capability.Team), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(10*time.Minute + time.Nanosecond)
	before := accountMutationRows(t, s)
	if _, err := s.UnlinkAccountIdentity(t.Context(), a, ProviderGoogle); err == nil || !reflect.DeepEqual(before, accountMutationRows(t, s)) {
		t.Fatalf("stale unlink changed state: %v", err)
	}
	*now = now.Add(-time.Nanosecond)
	ended, err := s.UnlinkAccountIdentity(t.Context(), a, ProviderGoogle)
	if err != nil || ended != 1 {
		t.Fatalf("unlink ended=%d: %v", ended, err)
	}
	if _, err := s.LookupSession(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(t.Context(), other); err == nil {
		t.Fatal("other session survived unlink")
	}
	if _, err := s.UnlinkAccountIdentity(t.Context(), a, ProviderGitHub); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("last method unlink: %v", err)
	}
}

func accountMutationInvitation(t *testing.T, s *Store, now time.Time) Invitation {
	t.Helper()
	owner, err := s.ResolveSignIn(t.Context(), SignInProfile{Provider: ProviderGoogle, Subject: "owner", Email: "owner@example.test", EmailVerified: true}, SignUpConditions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _, err := s.CreateAccountSession(t.Context(), owner.Account, owner.PersonalTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(raw)}}
	a := teamAuthorization(t, s, actor, owner.PersonalTeam, "POST", "/api/v1/team/invitations")
	tenant, err := s.ForTeam(t.Context(), owner.PersonalTeam)
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := tenant.CreateAuthorizedInvitation(t.Context(), a, "member@example.test", RoleEditor, "invite")
	if err != nil {
		t.Fatal(err)
	}
	return invitation
}

func TestAccountInvitationCommitsMembershipAndSessionTogether(t *testing.T) {
	s, a, raw, now := accountMutationFixture(t)
	invitation := accountMutationInvitation(t, s, *now)
	before := accountMutationRows(t, s)
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_invitation_session BEFORE UPDATE OF team ON sessions BEGIN SELECT RAISE(ABORT,'session refused'); END`)
	if _, err := s.AcceptAccountInvitation(t.Context(), a, invitation.ID); err == nil || !reflect.DeepEqual(before, accountMutationRows(t, s)) {
		t.Fatalf("invitation escaped session rollback: %v", err)
	}
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_invitation_session`)
	rewriteSchemaExec(t, s.db, `UPDATE accounts SET email_verified=0 WHERE id=?`, a.Principal.Capability.Subject.ID)
	if _, err := s.AcceptAccountInvitation(t.Context(), a, invitation.ID); !errors.Is(err, ErrEmailMismatch) {
		t.Fatalf("unverified acceptance: %v", err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE accounts SET email_verified=1 WHERE id=?`, a.Principal.Capability.Subject.ID)
	accepted, err := s.AcceptAccountInvitation(t.Context(), a, invitation.ID)
	if err != nil || accepted.Team != invitation.Team {
		t.Fatalf("accepted=%+v: %v", accepted, err)
	}
	session, err := s.LookupSession(t.Context(), raw)
	if err != nil || session.Team != invitation.Team {
		t.Fatalf("selected session=%+v: %v", session, err)
	}
	account, err := s.Account(t.Context(), session.AccountID)
	if err != nil || account.ActiveTeam != invitation.Team {
		t.Fatalf("selected account=%+v: %v", account, err)
	}
	a.Principal, err = s.ResolveAuthority(t.Context(), a.Principal.Authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptAccountInvitation(t.Context(), a, invitation.ID); !errors.Is(err, ErrInvitationClosed) {
		t.Fatalf("invitation replay: %v", err)
	}
}

func TestAccountInvitationHasOneWinnerAcrossSessions(t *testing.T) {
	s, a, _, now := accountMutationFixture(t)
	invitation := accountMutationInvitation(t, s, *now)
	raw, _, _, err := s.CreateAccountSession(t.Context(), Account{ID: a.Principal.Capability.Subject.ID}, Team(a.Principal.Capability.Team), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b := a
	b.Principal, err = s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(raw)})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, actor := range []capability.Authorized{a, b} {
		wg.Go(func() {
			_, err := s.AcceptAccountInvitation(t.Context(), actor, invitation.ID)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrInvitationClosed) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("invitation committed %d times", winners)
	}
}

func TestAccountInvitationExpiresOnStoreClock(t *testing.T) {
	for _, elapsed := range []time.Duration{0, time.Second} {
		t.Run(elapsed.String(), func(t *testing.T) {
			s, a, _, now := accountMutationFixture(t)
			invitation := accountMutationInvitation(t, s, *now)
			*now = invitation.ExpiresAt.Add(elapsed)
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET expires_at=? WHERE hash=?`, now.Add(time.Hour).UnixNano(), a.Principal.Authority.ID)
			before := accountMutationRows(t, s)
			if _, err := s.AcceptAccountInvitation(t.Context(), a, invitation.ID); !errors.Is(err, ErrInvitationClosed) || !reflect.DeepEqual(before, accountMutationRows(t, s)) {
				t.Fatalf("acceptance at %s: %v", elapsed, err)
			}
		})
	}
}

func TestAccountMutationsKeepNanosecondTimestamps(t *testing.T) {
	s, a, _, now := accountMutationFixture(t)
	id := a.Principal.Capability.Subject.ID
	account, err := s.Account(t.Context(), id)
	if err != nil || !account.CreatedAt.Equal(*now) {
		t.Fatalf("created=%s, want %s: %v", account.CreatedAt, *now, err)
	}
	*now = now.Add(457 * time.Nanosecond)
	created, err := s.CreateAccountTeam(t.Context(), a, TeamInfo{Slug: "timed"})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT updated_at FROM accounts WHERE id=?`,
		`SELECT created_at FROM memberships WHERE account_id=? AND team='timed'`,
	} {
		var at int64
		if err := s.db.QueryRow(query, id).Scan(&at); err != nil || at != now.UnixNano() {
			t.Fatalf("timestamp=%d, want %d: %v", at, now.UnixNano(), err)
		}
	}
	linked, err := s.LinkIdentity(t.Context(), id, SignInProfile{Provider: ProviderGitHub, Subject: "timed"}, *now)
	if err != nil || !linked.CreatedAt.Equal(*now) {
		t.Fatalf("linked=%s, want %s: %v", linked.CreatedAt, *now, err)
	}
	identities, err := s.AccountIdentities(t.Context(), id)
	if err != nil || len(identities) != 2 || !identities[1].CreatedAt.Equal(*now) {
		t.Fatalf("identity times=%+v: %v", identities, err)
	}
	a.Principal, err = s.ResolveAuthority(t.Context(), a.Principal.Authority)
	if err != nil || a.Principal.Capability.Team != string(created.Slug) {
		t.Fatal(err)
	}
	if _, err := s.UnlinkAccountIdentity(t.Context(), a, ProviderGitHub); err != nil {
		t.Fatal(err)
	}
	var at int64
	if err := s.db.QueryRow(`SELECT unlinked_at FROM identity_unlinks WHERE account_id=?`, id).Scan(&at); err != nil || at != now.UnixNano() {
		t.Fatalf("unlinked=%d, want %d: %v", at, now.UnixNano(), err)
	}
	counts, err := s.SignUpCounts(t.Context(), now.Add(time.Hour))
	if err != nil || counts.LastHour != 0 || counts.LastDay != 1 {
		t.Fatalf("signup count=%+v: %v", counts, err)
	}
}

func TestSessionCannotSelectRemovedTeamAfterFreshSessionRejoins(t *testing.T) {
	for _, previouslySelected := range []bool{false, true} {
		t.Run(fmt.Sprint("previously-selected=", previouslySelected), func(t *testing.T) {
			s, member, raw, now := accountMutationFixture(t)
			accountID := member.Principal.Capability.Subject.ID
			personal := Team(member.Principal.Capability.Team)
			invitation := accountMutationInvitation(t, s, *now)
			if _, err := s.AcceptAccountInvitation(t.Context(), member, invitation.ID); err != nil {
				t.Fatal(err)
			}
			if !previouslySelected {
				var err error
				raw, _, _, err = s.CreateAccountSession(t.Context(), Account{ID: accountID}, personal, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				member.Principal, err = s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(raw)})
				if err != nil {
					t.Fatal(err)
				}
			}
			var ownerID string
			if err := s.db.QueryRow(`SELECT account_id FROM memberships WHERE team=? AND role='owner'`, invitation.Team).Scan(&ownerID); err != nil {
				t.Fatal(err)
			}
			ownerRaw, _, _, err := s.CreateAccountSession(t.Context(), Account{ID: ownerID}, invitation.Team, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			owner := Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(ownerRaw)}}
			tenant := &Tenant{s: s, team: invitation.Team}
			if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: accountID}); err != nil {
				t.Fatal(err)
			}
			old, err := s.LookupSession(t.Context(), raw)
			if err != nil || old.Team != personal {
				t.Fatalf("old session after removal: %+v %v", old, err)
			}
			freshRaw, _, _, err := s.CreateAccountSession(t.Context(), Account{ID: accountID}, personal, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			ownerAuth := teamAuthorization(t, s, owner, invitation.Team, "POST", "/api/v1/team/invitations")
			rejoin, err := tenant.CreateAuthorizedInvitation(t.Context(), ownerAuth, "member@example.test", RoleEditor, "rejoin")
			if err != nil {
				t.Fatal(err)
			}
			fresh := member
			fresh.Principal, err = s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(freshRaw)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.AcceptAccountInvitation(t.Context(), fresh, rejoin.ID); err != nil {
				t.Fatal(err)
			}
			if current, err := s.LookupSession(t.Context(), freshRaw); err != nil || current.Team != invitation.Team {
				t.Fatalf("fresh session rejoin: %+v %v", current, err)
			}
			member.Principal, err = s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(raw)})
			if err != nil {
				t.Fatal(err)
			}
			before := accountMutationRows(t, s)
			if err := s.SelectAccountTeam(t.Context(), member, invitation.Team); err == nil {
				t.Fatal("old session regained removed team's authority")
			}
			if !reflect.DeepEqual(before, accountMutationRows(t, s)) {
				t.Fatal("refused selection changed state")
			}
			if old, err := s.LookupSession(t.Context(), raw); err != nil || old.Team != personal {
				t.Fatalf("refused selection moved session: %+v %v", old, err)
			}
			deletion := teamAuthorization(t, s, Actor{Authority: member.Principal.Authority}, personal, "DELETE", "/api/v1/team")
			if _, err := (&Tenant{s: s, team: personal}).DeleteAuthorizedTeam(t.Context(), deletion); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LookupSession(t.Context(), raw); err == nil {
				t.Fatal("automatic fallback refreshed a removed team's session epoch")
			}
		})
	}
}

func TestSessionFallsBackToTeamJoinedThroughAnotherSession(t *testing.T) {
	s, original, raw, _ := accountMutationFixture(t)
	personal := Team(original.Principal.Capability.Team)
	freshRaw, _, _, err := s.CreateAccountSession(t.Context(), Account{ID: original.Principal.Capability.Subject.ID}, personal, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fresh := original
	fresh.Principal, err = s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(freshRaw)})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := s.CreateAccountTeam(t.Context(), fresh, TeamInfo{Slug: "joined"})
	if err != nil {
		t.Fatal(err)
	}
	deletion := teamAuthorization(t, s, Actor{Authority: original.Principal.Authority}, personal, "DELETE", "/api/v1/team")
	if _, err := (&Tenant{s: s, team: personal}).DeleteAuthorizedTeam(t.Context(), deletion); err != nil {
		t.Fatal(err)
	}
	session, err := s.LookupSession(t.Context(), raw)
	if err != nil || session.Team != joined.Slug {
		t.Fatalf("fallback to newly joined team: %+v %v", session, err)
	}
}
