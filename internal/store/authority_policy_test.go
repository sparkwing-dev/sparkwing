package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestMemberIssuanceExpiryAndSessionParent(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "member", RoleEditor)
	issued, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &actor.Authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
	if err != nil {
		t.Fatal(err)
	}
	c := issued.Capability
	if c.Parent == nil || *c.Parent != actor.Authority || c.ExpiresAt == nil || c.ExpiresAt.Sub(c.IssuedAt) > 90*24*time.Hour {
		t.Fatalf("unbounded or detached member credential: %+v", c)
	}
	if err := (&Tenant{s: s, team: "a"}).RevokeAuthority(t.Context(), actor, actor.Authority); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}); err == nil {
		t.Fatal("member token survived logout")
	}
}

func TestMemberIssuanceRefusesImplicitAdministrationAndExcessLifetime(t *testing.T) {
	for _, kind := range []string{"implicit administration", "long lifetime"} {
		t.Run(kind, func(t *testing.T) {
			s := authorityStore(t)
			actor := authorityMember(t, s, "owner", RoleOwner)
			grants := []capability.Grant{capability.TeamAdmin}
			var expiry *time.Time
			if kind == "long lifetime" {
				grants = []capability.Grant{capability.RunsRead}
				at := time.Now().Add(91 * 24 * time.Hour)
				expiry = &at
			}
			var before int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM capabilities`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &actor.Authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: grants, ExpiresAt: expiry}); err == nil {
				t.Fatal("accepted member issuance outside policy")
			}
			rewriteSchemaCount(t, s.db, "capabilities", before)
		})
	}
}

func TestMembershipEpochPreventsCredentialRevivalAfterRejoin(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	member := authorityMember(t, s, "member", RoleEditor)
	issued, err := s.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &member.Authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member"}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','editor',1)`)
	for _, ref := range []capability.AuthorityRef{member.Authority, {Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}} {
		if _, err := s.ResolveAuthority(t.Context(), ref); err == nil {
			t.Fatalf("old authority revived: %+v", ref)
		}
	}
}

func TestPersonalAuthorityFollowsRequiredSettingAndGrant(t *testing.T) {
	for _, kind := range []string{"setting", "grant", "shared"} {
		t.Run(kind, func(t *testing.T) {
			s := authorityStore(t)
			actor := authorityMember(t, s, "registrar", RoleEditor)
			personal := kind != "shared"
			requirement := &authorityRequirement{Account: "registrar", Grant: capability.RunsWrite, MembersCanAddMachines: true}
			if !personal {
				requirement = nil
			}
			c := authorityRoot("runner", "a")
			tx, err := s.beginTx(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			origin := &CapabilityOrigin{Kind: capability.ProvisionMachine, RegistrationID: "runner:a:runner", Personal: personal, Requirement: requirement}
			if err := persistCapabilityTx(t.Context(), tx, c, origin); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if kind == "setting" {
				rewriteSchemaExec(t, s.db, `UPDATE teams SET members_can_add_machines=0 WHERE name='a'`)
			} else {
				rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE team='a' AND account_id='registrar'`)
			}
			_, err = s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix})
			if (err == nil) != (!personal) {
				t.Fatalf("%s authority: %v (actor=%v)", kind, err, actor)
			}
		})
	}
}

func TestOIDCCeilingIncludesEveryAncestorGraceAndDeadline(t *testing.T) {
	now := time.Unix(1700000000, 0)
	expiry, grace, deadline := now.Add(time.Hour), now.Add(time.Second), now.Add(time.Minute)
	for _, at := range []*time.Time{&expiry, &grace, &deadline} {
		chain := []authorityNode{{principal: capability.Principal{Capability: capability.Capability{ExpiresAt: &expiry}}}, {principal: capability.Principal{Capability: capability.Capability{RevokedAt: at}}}}
		if got := authorityNotAfter(chain, now.Add(2*time.Hour)); !got.Equal(*at) {
			t.Fatalf("ancestor bound %v: got %v", at, got)
		}
	}
}
