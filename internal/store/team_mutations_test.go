package store

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func teamAuthorization(t *testing.T, s *Store, actor Actor, team Team, method, path string) capability.Authorized {
	t.Helper()
	p, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	route, ok := capability.LookupRoute(method, path)
	if !ok {
		t.Fatal("missing route", path)
	}
	target := capability.Target{Team: string(team)}
	a, err := capability.Authorize(t.Context(), s.AsOperator(), route, capability.Authenticated{Principal: p, Method: method, Pattern: path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, target)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTeamMutationsRequireCurrentAuthorityProperty(t *testing.T) {
	property := func(caseID uint8, suffix uint32) bool {
		s := authorityStore(t)
		actor := authorityMember(t, s, "owner", RoleOwner)
		tenant := &Tenant{s: s, team: "a"}
		if suffix%2 == 0 {
			child := authorityRoot("delegated", "a")
			child.Parent = &actor.Authority
			child.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}
			child.Grants = []capability.Grant{capability.TeamAdmin, capability.RunsControl}
			authorityPersist(t, s, child)
			actor.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}
		}
		a := teamAuthorization(t, s, actor, "a", "PATCH", "/api/v1/team")
		rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','owner','owner',1)`)
		switch caseID % 7 {
		case 0:
			a.Target.Team = fmt.Sprint("foreign-", suffix)
		case 1:
			rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE team='a'`)
		case 2:
			rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='reader' WHERE team='a'`)
		case 3:
			rewriteSchemaExec(t, s.db, `DELETE FROM sessions WHERE hash='owner'`)
		case 4:
			a.Access.Grants = []capability.Grant{capability.RunsRead}
		case 6:
			s.clock = func() time.Time { return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }
		case 5:
			a.Principal.Authority.ID = fmt.Sprint("foreign-", suffix)
		}
		before := purgeRows(t, s, "a", true)
		_, rename := tenant.RenameAuthorizedTeam(t.Context(), a, "forged")
		_, deletion := tenant.DeleteAuthorizedTeam(t.Context(), a)
		_, invite := tenant.CreateAuthorizedInvitation(t.Context(), a, "guest@example.test", RoleReader, "key")
		withdraw := tenant.DeleteAuthorizedInvitation(t.Context(), a, "missing")
		trust := tenant.SetAuthorizedBuildTrust(t.Context(), a, true)
		a.Target.RunID = "missing"
		if caseID%7 != 4 {
			a.Access.Grants = []capability.Grant{capability.RunsControl}
		}
		prune := tenant.DeleteAuthorizedRun(t.Context(), a)
		return rename != nil && deletion != nil && invite != nil && withdraw != nil && trust != nil && prune != nil && reflect.DeepEqual(before, purgeRows(t, s, "a", true))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 36}); err != nil {
		t.Fatal(err)
	}
}

func TestTeamMutationsCommitStoreTimeAndInvitationReplay(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "owner", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	now := time.Unix(1800000000, 123).UTC()
	s.clock = func() time.Time { return now }
	a := teamAuthorization(t, s, actor, "a", "PATCH", "/api/v1/team")
	info, err := tenant.RenameAuthorizedTeam(t.Context(), a, "  Team A  ")
	if err != nil || info.Slug != "a" || info.DisplayName != "Team A" {
		t.Fatalf("rename=%+v: %v", info, err)
	}
	var updated int64
	if err := s.db.QueryRow(`SELECT updated_at FROM teams WHERE name='a'`).Scan(&updated); err != nil || updated != now.UnixNano() {
		t.Fatalf("updated=%d: %v", updated, err)
	}
	if err := tenant.SetAuthorizedBuildTrust(t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	if trust, err := tenant.TrustLocalBuilds(t.Context()); err != nil || !trust {
		t.Fatalf("trust=%v: %v", trust, err)
	}
	first, err := tenant.CreateAuthorizedInvitation(t.Context(), a, "  GUEST@example.test ", RoleEditor, "stable")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	second, err := tenant.CreateAuthorizedInvitation(t.Context(), a, "guest@example.test", RoleEditor, "stable")
	if err != nil || !reflect.DeepEqual(first, second) || first.InvitedBy != "owner" || !first.CreatedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("replay=%+v, first=%+v: %v", second, first, err)
	}
	_, err = tenant.CreateAuthorizedInvitation(t.Context(), a, "different@example.test", RoleEditor, "stable")
	var conflict *apierr.Error
	if !errors.As(err, &conflict) || conflict.Code != apierr.Conflict {
		t.Fatalf("changed replay: %v", err)
	}
	if err := tenant.DeleteAuthorizedInvitation(t.Context(), a, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err = tenant.CreateAuthorizedInvitation(t.Context(), a, first.Email, first.Role, "stable")
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("withdrawn replay=%+v: %v", second, err)
	}
	rewriteSchemaCount(t, s.db, "invitations", 1)
}

func TestTeamDeletionUsesCurrentOwnerAndLastTeamGuards(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "owner", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	a := teamAuthorization(t, s, actor, "a", "DELETE", "/api/v1/team")
	if _, err := tenant.DeleteAuthorizedTeam(t.Context(), a); !errors.Is(err, ErrOnlyTeam) {
		t.Fatalf("last team: %v", err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','owner','owner',1)`)
	now := time.Unix(1800000000, 123).UTC()
	s.clock = func() time.Time { return now }
	deletion, err := tenant.DeleteAuthorizedTeam(t.Context(), a)
	if err != nil || deletion.Team != "a" || deletion.State != TeamDeletionPending || !deletion.RequestedAt.Equal(now) {
		t.Fatalf("deletion=%+v: %v", deletion, err)
	}
	var requested string
	if err := s.db.QueryRow(`SELECT requested_by FROM team_deletions WHERE slug='a'`).Scan(&requested); err != nil || requested != "owner" {
		t.Fatalf("requested=%q: %v", requested, err)
	}
	if p, err := s.AsOperator().ResolveAuthority(t.Context(), actor.Authority); err != nil || p.Capability.Team != "b" {
		t.Fatalf("session did not leave deleted team: %+v: %v", p.Capability.Team, err)
	}
	var members int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE team='b'`).Scan(&members); err != nil || members != 1 {
		t.Fatalf("other membership=%d: %v", members, err)
	}
}

func TestTeamDeletionRetainsCheckoutAndHoldGuardsOnStoreClock(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "owner", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','owner','owner',1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	now := time.Unix(1800000000, 123).UTC()
	s.clock = func() time.Time { return now }
	a := teamAuthorization(t, s, actor, "a", "DELETE", "/api/v1/team")
	if _, err := tenant.TestOnlyOpenCreditCheckout(t.Context(), 100*MicroCreditsPerCent, now, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.DeleteAuthorizedTeam(t.Context(), a); !errors.Is(err, ErrOpenCheckout) {
		t.Fatalf("open checkout: %v", err)
	}
	now = now.Add(time.Hour)
	if _, err := tenant.hold(t.Context(), "dispute", "", "hold", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.DeleteAuthorizedTeam(t.Context(), a); !errors.Is(err, ErrTeamFrozen) {
		t.Fatalf("hold: %v", err)
	}
	if _, err := tenant.ReleaseCreditFreezes(t.Context(), "dispute", "", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.DeleteAuthorizedTeam(t.Context(), a); err != nil {
		t.Fatal(err)
	}
}

func TestTeamInvitationRefusalRollsBackAndConcurrentReplayHasOneRow(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "owner", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	a := teamAuthorization(t, s, actor, "a", "POST", "/api/v1/team/invitations")
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_invitation BEFORE INSERT ON invitations BEGIN SELECT RAISE(ABORT,'refused'); END`)
	if out, err := tenant.CreateAuthorizedInvitation(t.Context(), a, "guest@example.test", RoleEditor, "key"); err == nil || out.ID != "" {
		t.Fatalf("failed insert=%+v: %v", out, err)
	}
	rewriteSchemaCount(t, s.db, "invitations", 0)
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_invitation`)
	results := make(chan Invitation, 8)
	failures := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			inv, err := tenant.CreateAuthorizedInvitation(t.Context(), a, "guest@example.test", RoleEditor, "key")
			results <- inv
			failures <- err
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first Invitation
	for inv := range results {
		if first.ID == "" {
			first = inv
		}
		if !reflect.DeepEqual(first, inv) {
			t.Fatal("concurrent replay changed invitation")
		}
	}
	rewriteSchemaCount(t, s.db, "invitations", 1)
}
