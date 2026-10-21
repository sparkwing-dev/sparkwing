package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestMemberIssuanceExpiryAndSessionParent(t *testing.T) {
	s := authorityStore(t)
	actor := authorityMember(t, s, "member", RoleEditor)
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &actor.Authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
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
	if _, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}); err == nil {
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
			if _, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &actor.Authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "owner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: grants, ExpiresAt: expiry}); err == nil {
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
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &member.Authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member"}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','editor',1)`)
	for _, ref := range []capability.AuthorityRef{member.Authority, {Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}} {
		if _, err := s.AsOperator().ResolveAuthority(t.Context(), ref); err == nil {
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
			if err := tx.operator.persistCapabilityTx(t.Context(), tx, c, origin); err != nil {
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
			_, err = s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix})
			if (err == nil) != (!personal) {
				t.Fatalf("%s authority: %v (actor=%v)", kind, err, actor)
			}
		})
	}
}

func TestOIDCCeilingIncludesEveryAncestorGraceAndDeadline(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name  string
		bound func(*capability.Capability, *time.Time)
	}{
		{"expiry", func(c *capability.Capability, at *time.Time) { c.ExpiresAt = at }},
		{"revocation grace", func(c *capability.Capability, at *time.Time) { c.RevokedAt = at }},
		{"binding deadline", func(c *capability.Capability, at *time.Time) { c.Binding.Deadline = at }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound := now.Add(time.Minute)
			future := now.Add(time.Hour)
			ancestor := capability.Capability{}
			tc.bound(&ancestor, &bound)
			chain := []authorityNode{{principal: capability.Principal{Capability: capability.Capability{ExpiresAt: &future}}}, {principal: capability.Principal{Capability: ancestor}}}
			if got := authorityNotAfter(chain, now.Add(2*time.Hour)); !got.Equal(bound) {
				t.Fatalf("ancestor bound: got %v, want %v", got, bound)
			}
		})
	}
}

func TestMemberTokenDemotionPermanentlyFencesAuthorityAfterSessionTimeout(t *testing.T) {
	s, _, now := sessionRuntimeStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	member := Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "member"}}
	rewriteSchemaExec(t, s.db, `UPDATE sessions SET expires_at=? WHERE hash=?`, now.Add(time.Hour).UnixNano(), member.Authority.ID)
	issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &member.Authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Hour)
	if _, err := s.AsOperator().LookupCapability(t.Context(), issued.Capability.Digest); err != nil {
		t.Fatalf("timeout expired token: %v", err)
	}
	tenant := &Tenant{s: s, team: "a"}
	for _, role := range []Role{RoleReader, RoleEditor} {
		if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member", Role: &role}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AsOperator().LookupCapability(t.Context(), issued.Capability.Digest); err == nil {
			t.Fatalf("old token retained authority after role change to %s", role)
		}
	}
}

func TestMemberTokenRotationEnforcesExpiryCeiling(t *testing.T) {
	for _, excess := range []time.Duration{0, time.Nanosecond} {
		t.Run(excess.String(), func(t *testing.T) {
			s, _, now := sessionRuntimeStore(t)
			member := Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "member"}}
			old := inventoryMemberRoot(t, s, member)
			expiry := now.Add(90*24*time.Hour + excess)
			tenant := &Tenant{s: s, team: "a"}
			rotation, err := tenant.RotateCapability(t.Context(), inventoryAuthorization(t, s, member, "a", old.Capability.Prefix), 0, &expiry)
			if excess == 0 {
				if err != nil || rotation.Issued.Capability.ExpiresAt == nil || !rotation.Issued.Capability.ExpiresAt.Equal(expiry) {
					t.Fatalf("expiry at ceiling: %+v %v", rotation.Issued.Capability, err)
				}
			} else {
				if err == nil || rotation.Issued.Raw != "" {
					t.Fatal("rotation exceeded member expiry ceiling")
				}
				rewriteSchemaCount(t, s.db, "capabilities", 1)
			}
		})
	}
}
