package store

import (
	"crypto/sha256"
	"errors"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func reissueFixture(t *testing.T) (*Store, capability.Principal, capability.Issued) {
	t.Helper()
	s := authorityStore(t)
	spec := bootstrapSpec(sha256.Sum256([]byte(t.Name())))
	if _, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	operator, err := s.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(spec.Seed)))
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.AsOperator().CreateOperatorTeam(t.Context(), capability.Authorized{Principal: operator}, "synthetic", "Synthetic monitoring")
	if err != nil {
		t.Fatal(err)
	}
	return s, operator, root
}

func lookupRaw(t *testing.T, s *Store, raw string) (capability.Principal, error) {
	t.Helper()
	return s.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
}

func delegateFrom(t *testing.T, s *Store, parent capability.Issued) capability.Issued {
	t.Helper()
	child, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Capability.Prefix}, Audience: capability.Controller, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: parent.Capability.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func TestOperatorReissuesHeadlessTeamAdmin(t *testing.T) {
	s, operator, root := reissueFixture(t)
	got, err := s.AsOperator().ReissueOperatorTeamAdmin(t.Context(), capability.Authorized{Principal: operator}, "synthetic", false)
	if err != nil {
		t.Fatal(err)
	}
	c := got.Issued.Capability
	if c.Prefix == root.Capability.Prefix || c.Team != "synthetic" || c.Subject != root.Capability.Subject || c.Parent != nil || !slices.Equal(c.Grants, root.Capability.Grants) || len(got.Revoked) != 0 {
		t.Fatalf("reissued=%+v revoked=%v", c, got.Revoked)
	}
	if want := s.now().Add(adminTokenLifetime); c.ExpiresAt == nil || !c.ExpiresAt.Equal(want) {
		t.Fatalf("expiry=%v want %v", c.ExpiresAt, want)
	}
	for _, raw := range []string{got.Issued.Raw, root.Raw} {
		if _, err := lookupRaw(t, s, raw); err != nil {
			t.Fatalf("credential ended without --revoke-previous: %v", err)
		}
	}
	tenant, err := s.ForTeam(t.Context(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := s.db.QueryRow(`SELECT actor FROM audit_events WHERE team='synthetic' AND kind='team.mutated' AND subject_id=?`, c.Subject.ID).Scan(&actor); err != nil || actor == "" || actor == c.Subject.ID {
		t.Fatalf("reissue audit actor=%q err=%v", actor, err)
	}
	created, err := tenant.BusinessEvents(t.Context(), BusinessEventTeamCreated, 10, "")
	if err != nil || len(created) != 1 || created[0].Actor != actor {
		t.Fatalf("reissue audit actor %q differs from team creation %+v %v", actor, created, err)
	}
}

func TestOperatorTeamAdminReissueRequiresOperatorTokens(t *testing.T) {
	s, operator, root := reissueFixture(t)
	admin, err := lookupRaw(t, s, root.Raw)
	if err != nil {
		t.Fatal(err)
	}
	limited, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: operator.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.OperatorLimits}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	limitedPrincipal, err := lookupRaw(t, s, limited.Raw)
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]capability.Principal{"team admin": admin, "operator without operator.tokens": limitedPrincipal} {
		got, err := s.AsOperator().ReissueOperatorTeamAdmin(t.Context(), capability.Authorized{Principal: p}, "synthetic", true)
		if err == nil || got.Issued.Raw != "" {
			t.Fatalf("%s reissued an admin credential", name)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM capabilities WHERE team='synthetic' AND parent_id IS NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("team roots=%d %v", count, err)
	}
	if _, err := lookupRaw(t, s, root.Raw); err != nil {
		t.Fatalf("refused reissue revoked the root: %v", err)
	}
}

func TestOperatorTeamAdminReissueRefusesTeamsWithPeople(t *testing.T) {
	cases := map[string][]string{
		"human owner": {
			`INSERT INTO accounts(id,email,name,active_team,created_at,updated_at) VALUES ('person','person@example.test','Person','synthetic',1,1)`,
			`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('synthetic','person','owner',1)`,
		},
		"human reader without email": {
			`INSERT INTO accounts(id,email,name,active_team,created_at,updated_at) VALUES ('person','','Person','synthetic',1,1)`,
			`INSERT INTO identities(provider,subject,account_id,created_at) VALUES ('github','501','person',1)`,
			`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('synthetic','person','reader',1)`,
		},
		"creator linked a sign-in": {
			`INSERT INTO identities(provider,subject,account_id,created_at) SELECT 'github','502',created_by,1 FROM teams WHERE name='synthetic'`,
		},
		"open invitation": {
			`INSERT INTO invitations(id,team,email,role,invited_by,created_at,expires_at) SELECT 'invite','synthetic','owner@example.test','owner',created_by,1,9223372036854775807 FROM teams WHERE name='synthetic'`,
		},
		"creator no longer owns the team": {
			`UPDATE memberships SET role='editor' WHERE team='synthetic'`,
		},
	}
	for name, statements := range cases {
		t.Run(name, func(t *testing.T) {
			s, operator, root := reissueFixture(t)
			for _, statement := range statements {
				rewriteSchemaExec(t, s.db, statement)
			}
			got, err := s.AsOperator().ReissueOperatorTeamAdmin(t.Context(), capability.Authorized{Principal: operator}, "synthetic", true)
			var api *apierr.Error
			if !errors.As(err, &api) || api.Code != apierr.Conflict || got.Issued.Raw != "" {
				t.Fatalf("reissue into a team with people: %+v %v", got, err)
			}
			if _, err := lookupRaw(t, s, root.Raw); err != nil {
				t.Fatalf("refused reissue revoked the root: %v", err)
			}
		})
	}
}

func TestOperatorTeamAdminReissueIgnoresClosedInvitations(t *testing.T) {
	s, operator, _ := reissueFixture(t)
	for _, statement := range []string{
		`INSERT INTO invitations(id,team,email,role,invited_by,created_at,expires_at,withdrawn_at) SELECT 'withdrawn','synthetic','a@example.test','owner',created_by,1,9223372036854775807,2 FROM teams WHERE name='synthetic'`,
		`INSERT INTO invitations(id,team,email,role,invited_by,created_at,expires_at) SELECT 'expired','synthetic','b@example.test','owner',created_by,1,2 FROM teams WHERE name='synthetic'`,
	} {
		rewriteSchemaExec(t, s.db, statement)
	}
	if _, err := s.AsOperator().ReissueOperatorTeamAdmin(t.Context(), capability.Authorized{Principal: operator}, "synthetic", false); err != nil {
		t.Fatalf("closed invitations blocked a reissue: %v", err)
	}
}

func TestOperatorTeamAdminReissueRevokesPreviousLineage(t *testing.T) {
	s, operator, root := reissueFixture(t)
	child := delegateFrom(t, s, root)
	grandchild := delegateFrom(t, s, child)
	other, err := s.AsOperator().CreateOperatorTeam(t.Context(), capability.Authorized{Principal: operator}, "other-team", "")
	if err != nil {
		t.Fatal(err)
	}
	otherChild := delegateFrom(t, s, other)
	got, err := s.AsOperator().ReissueOperatorTeamAdmin(t.Context(), capability.Authorized{Principal: operator}, "synthetic", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{root.Capability.Prefix, child.Capability.Prefix, grandchild.Capability.Prefix}
	if !slices.Equal(got.Revoked, want) {
		t.Fatalf("revoked=%v want %v", got.Revoked, want)
	}
	for _, ended := range []capability.Issued{root, child, grandchild} {
		if _, err := lookupRaw(t, s, ended.Raw); err == nil {
			t.Fatalf("%s survived --revoke-previous", ended.Capability.Prefix)
		}
		var revokedAt *int64
		if err := s.db.QueryRow(`SELECT revoked_at FROM capabilities WHERE prefix=?`, ended.Capability.Prefix).Scan(&revokedAt); err != nil || revokedAt == nil {
			t.Fatalf("%s row not revoked: %v", ended.Capability.Prefix, err)
		}
	}
	for _, live := range []capability.Issued{got.Issued, other, otherChild} {
		if _, err := lookupRaw(t, s, live.Raw); err != nil {
			t.Fatalf("%s ended: %v", live.Capability.Prefix, err)
		}
	}
	var audited int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE team='synthetic' AND kind='team.mutated' AND subject_id LIKE 'capability:%'`).Scan(&audited); err != nil || audited != 3 {
		t.Fatalf("revocation audits=%d %v", audited, err)
	}
	again, err := s.AsOperator().ReissueOperatorTeamAdmin(t.Context(), capability.Authorized{Principal: operator}, "synthetic", true)
	if err != nil || !slices.Equal(again.Revoked, []string{got.Issued.Capability.Prefix}) {
		t.Fatalf("second reissue revoked %v, want only the live root: %v", again.Revoked, err)
	}
}

func TestOperatorTeamAdminReissueRefusesUnknownTeam(t *testing.T) {
	s, operator, _ := reissueFixture(t)
	for _, team := range []string{"absent-team", "../synthetic"} {
		if _, err := s.AsOperator().ReissueOperatorTeamAdmin(t.Context(), capability.Authorized{Principal: operator}, team, false); err == nil {
			t.Fatalf("reissued into %q", team)
		}
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO team_deletions(slug,state,requested_at,requested_by) VALUES ('synthetic','pending',1,'operator')`)
	if _, err := s.AsOperator().ReissueOperatorTeamAdmin(t.Context(), capability.Authorized{Principal: operator}, "synthetic", false); !errors.Is(err, ErrUnknownTeam) {
		t.Fatalf("retired team: %v", err)
	}
}
