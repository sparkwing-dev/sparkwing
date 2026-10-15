package store

import (
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestMembershipRevokesOnlyTheAffectedMintersCredentialsProperty(t *testing.T) {
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
				authorityPersist(t, s, c)
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
				team := Team("a")
				if prefix == "foreign" {
					team = "b"
				} else if prefix == "deployment" {
					team = ""
				}
				_, err := s.AuthGenerations(t.Context(), ref, string(team))
				ended := (role == nil || keep == RoleReader) && (prefix == "mine" || prefix == "child")
				if (err != nil) != ended {
					t.Fatalf("prefix=%s ended=%v error=%v", prefix, ended, err)
				}
			}
			var generation int
			if err := s.db.QueryRow("SELECT revocation_generation FROM capabilities WHERE prefix='mine'").Scan(&generation); err != nil {
				t.Fatal(err)
			}
			if generation != map[bool]int{false: 0, true: 1}[role == nil || keep == RoleReader] {
				t.Fatalf("root generation=%d", generation)
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
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_revocation BEFORE UPDATE ON capabilities BEGIN SELECT RAISE(ABORT,'refuse revocation'); END`)
	before := append(purgeRows(t, s, "a", true), purgeRows(t, s, "a", false)...)
	err := (&Tenant{s: s, team: "a"}).UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member"})
	if err == nil {
		t.Fatal("revocation failure committed membership removal")
	}
	after := append(purgeRows(t, s, "a", true), purgeRows(t, s, "a", false)...)
	if !slices.Equal(before, after) {
		t.Fatal("membership failure committed a partial effect")
	}
}

func TestMembershipDowngradeWithdrawsHigherInvitations(t *testing.T) {
	s := authorityStore(t)
	owner := authorityMember(t, s, "owner", RoleOwner)
	_ = authorityMember(t, s, "member", RoleEditor)
	tenant := &Tenant{s: s, team: "a"}
	for _, role := range []Role{RoleReader, RoleEditor} {
		if _, err := tenant.CreateInvitation(t.Context(), "member", string(role)+"@example.com", role, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	role := RoleReader
	if err := tenant.UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member", Role: &role}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{RoleReader, RoleEditor} {
		var withdrawn bool
		if err := s.db.QueryRow("SELECT withdrawn_at IS NOT NULL FROM invitations WHERE team='a' AND role=?", role).Scan(&withdrawn); err != nil {
			t.Fatal(err)
		}
		if withdrawn != (role == RoleEditor) {
			t.Fatalf("role=%s withdrawn=%v", role, withdrawn)
		}
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
				team, grants = "b", []capability.Grant{capability.RunsRead, capability.LogsRead}
			}
			parent := authorityRoot("parent", "a")
			parent.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: "member"}
			authorityPersist(t, s, parent)
			rewriteSchemaExec(t, s.db, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id,capability_prefix) VALUES ('backed','member','runs.read',1,?,'a','member','parent')`, time.Now().Add(time.Hour).UnixNano())
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
