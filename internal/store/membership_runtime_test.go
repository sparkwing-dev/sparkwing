package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestMembershipRevalidatesOnlyTheAffectedPersonalCredentialsProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(817, 719))
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := authorityStore(t)
			owner := authorityMember(t, s, "owner", RoleOwner)
			_ = authorityMember(t, s, "member", RoleEditor)
			tenant := &Tenant{s: s, team: "a"}
			for _, entry := range []struct{ prefix, team, creator string }{
				{"mine", "a", "member"}, {"owners", "a", "owner"}, {"foreign", "b", "member"}, {"deployment", "", "member"},
			} {
				c := authorityRoot(entry.prefix, entry.team)
				if c.Team == "" {
					c.Subject.Kind = capability.SubjectService
				}
				if random.IntN(2) == 0 {
					deadline := time.Now().Add(time.Hour)
					c.RevokedAt = &deadline
				}
				if entry.prefix == "mine" {
					rewriteSchemaExec(t, s.db, `UPDATE teams SET members_can_add_machines=1 WHERE name='a'`)
					tx, err := s.beginTx(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					origin := &CapabilityOrigin{Kind: capability.ProvisionMachine, RegistrationID: "runner:a:mine", Personal: true, Requirement: &authorityRequirement{Account: "member", Grant: capability.RunsWrite, MembersCanAddMachines: true}}
					if err := tx.operator.persistCapabilityTx(t.Context(), tx, c, origin); err != nil {
						_ = tx.Rollback()
						t.Fatal(err)
					}
					if err := tx.Commit(); err != nil {
						t.Fatal(err)
					}
				} else {
					authorityPersist(t, s, c)
				}
				rewriteSchemaExec(t, s.db, "UPDATE capabilities SET created_by=? WHERE prefix=?", entry.creator, entry.prefix)
			}
			child := authorityRoot("child", "a")
			child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "mine"}
			authorityPersist(t, s, child)
			credentials := func() []string {
				return slices.DeleteFunc(purgeRows(t, s, "a", false), func(row string) bool { return !strings.HasPrefix(row, "capabilities") })
			}
			before := credentials()
			keep := []Role{RoleReader, RoleOwner}[random.IntN(2)]
			var role *Role
			if random.IntN(2) == 0 {
				role = &keep
			}
			if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member", Role: role}); err != nil {
				t.Fatal(err)
			}
			if after := credentials(); !slices.Equal(before, after) {
				t.Fatal("membership update changed another team or deployment credential")
			}
			for _, prefix := range []string{"mine", "owners", "foreign", "deployment", "child"} {
				ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
				_, err := s.AsOperator().ResolveAuthority(t.Context(), ref)
				ended := (role == nil || keep == RoleReader) && (prefix == "mine" || prefix == "child")
				if (err != nil) != ended {
					t.Fatalf("prefix=%s ended=%v error=%v", prefix, ended, err)
				}
			}

		})
	}
}

func TestMembershipCredentialFailureRollsBackEveryEffect(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	_ = authorityMember(t, s, "member", RoleEditor)
	c := authorityRoot("machine", "a")
	authorityPersist(t, s, c)
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET created_by='member' WHERE prefix='machine'")
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_epoch BEFORE INSERT ON membership_epochs BEGIN SELECT RAISE(ABORT,'refuse epoch change'); END`)
	before := append(purgeRows(t, s, "a", true), purgeRows(t, s, "a", false)...)
	err := (&Tenant{s: s, team: "a"}).UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member"})
	if err == nil {
		t.Fatal("epoch failure committed membership removal")
	}
	after := append(purgeRows(t, s, "a", true), purgeRows(t, s, "a", false)...)
	if !slices.Equal(before, after) {
		t.Fatal("membership failure committed a partial effect")
	}
}

func TestMembershipDowngradeRechecksInviterAuthority(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	_ = authorityMember(t, s, "member", RoleOwner)
	tenant := &Tenant{s: s, team: "a"}
	invitations := map[string]string{}
	for _, role := range []Role{RoleReader, RoleEditor} {
		invitation, err := tenant.InviteForTest(t, t.Context(), "member", string(role)+"@example.com", role, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		invitations[string(role)] = invitation.ID
		invitee := authorityMember(t, s, string(role), RoleReader)
		rewriteSchemaExec(t, s.db, `UPDATE accounts SET email=?,email_verified=1 WHERE id=?`, string(role)+"@example.com", role)
		rewriteSchemaExec(t, s.db, `INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('google',?,?,?,1,1)`, role, role, string(role)+"@example.com")
		rewriteSchemaExec(t, s.db, `UPDATE memberships SET team='b' WHERE account_id=?`, role)
		rewriteSchemaExec(t, s.db, `UPDATE sessions SET team='b' WHERE hash=?`, invitee.Authority.ID)
		rewriteSchemaExec(t, s.db, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES (?,'b',0)`, invitee.Authority.ID)
	}
	owner.Authority = tenant.mutationFixtureAuthority(t, "owner", capability.TeamAdmin, time.Now()).Principal.Authority
	role := RoleReader
	if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member", Role: &role}); err != nil {
		t.Fatal(err)
	}
	before := append(purgeRows(t, s, "a", true), purgeRows(t, s, "a", false)...)
	for account, invitation := range invitations {
		principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: account})
		if err != nil {
			t.Fatal(err)
		}
		authorized := capability.Authorized{Principal: principal, Target: capability.Target{AccountID: account}, Access: capability.AccessRule{Credential: capability.CredentialAccount, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
		if _, err := s.AsOperator().AcceptAccountInvitation(t.Context(), authorized, invitation); !errors.Is(err, ErrInvitationClosed) {
			t.Fatalf("%s invitation accepted after inviter demotion: %v", account, err)
		}
	}
	after := append(purgeRows(t, s, "a", true), purgeRows(t, s, "a", false)...)
	if !slices.Equal(before, after) {
		t.Fatal("refused invitation changed memberships or invitation state")
	}
}

func TestMembershipRemovalKeepsAccountSessionsScopedAndParentsBoundProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(191, 919))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := authorityStore(t)
			owner := authorityMember(t, s, "owner", RoleOwner)
			member := authorityMember(t, s, "member", RoleEditor)
			var team string
			var grants []capability.Grant
			if random.IntN(2) == 0 {
				rewriteSchemaExec(t, s.db, "INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','member','reader',1)")
				rewriteSchemaExec(t, s.db, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES ('member','b',0)`)
				team, grants = "b", []capability.Grant{capability.RunsRead, capability.LogsRead}
			}
			parent := authorityRoot("parent", "a")
			parent.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "member"}
			authorityPersist(t, s, parent)
			rewriteSchemaExec(t, s.db, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id,capability_prefix) VALUES ('backed','member','runs.read',1,?,'a','member','parent')`, time.Now().Add(time.Hour).UnixNano())
			rewriteSchemaExec(t, s.db, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES ('backed','a',0)`)
			if err := (&Tenant{s: s, team: "a"}).UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member"}); err != nil {
				t.Fatal(err)
			}
			err := s.inTx(t.Context(), func(tx *storeTx) error {
				p, err := tx.ResolveAuthority(t.Context(), member.Authority)
				var failure *apierr.Error
				if team == "" {
					if !errors.As(err, &failure) || failure.Code != apierr.Unauthenticated {
						t.Fatalf("removed member session: %v", err)
					}
				} else if err != nil {
					return err
				} else if p.Capability.Team != team || !slices.Equal(p.Capability.Grants, grants) || p.Capability.Subject.ID != "member" {
					t.Fatalf("account session acquired foreign authority: %+v", p.Capability)
				}
				_, err = tx.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "backed"})
				if !errors.As(err, &failure) || failure.Code != apierr.Unauthenticated {
					t.Fatalf("revoked capability session: %v", err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var bound string
			if err := s.db.QueryRow("SELECT team FROM sessions WHERE hash='backed'").Scan(&bound); err != nil || bound != "a" {
				t.Fatalf("capability session moved teams: %q, %v", bound, err)
			}
		})
	}
}
