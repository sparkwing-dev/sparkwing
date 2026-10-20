package store

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestSessionDataDescendantsKeepTheirAuthenticatedTeamEpoch(t *testing.T) {
	for _, depth := range []int{1, 2} {
		for _, otherRole := range []Role{RoleReader, RoleEditor} {
			t.Run(fmt.Sprintf("%d/%s", depth, otherRole), func(t *testing.T) {
				s := authorityStore(t)
				s.SetSessionKeyCipher(fixtureSecretCipher(t))
				if _, err := s.csrfSigningKey(t.Context()); err != nil {
					t.Fatal(err)
				}
				owner := authorityMember(t, s, "owner", RoleOwner)
				authorityMember(t, s, "other-owner", RoleOwner, "b")
				member := authorityMember(t, s, "member", RoleEditor)
				rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','member',?,1)`, otherRole)
				scope := &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
				parent := member.Authority
				var issued capability.Issued
				for range depth {
					var err error
					issued, err = s.DelegateCapability(t.Context(), capability.DelegateSpec{
						Parent: parent, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone},
						Grants: []capability.Grant{capability.DataRead}, Repositories: []string{"gh:1"}, Data: scope,
					})
					if err != nil {
						t.Fatal(err)
					}
					parent = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}
				}
				selectTeam := func(team Team) error {
					p, err := s.ResolveAuthority(t.Context(), member.Authority)
					if err != nil {
						return err
					}
					return s.SelectAccountTeam(t.Context(), capability.Authorized{Principal: p, Access: capability.AccessRule{Credential: capability.CredentialAccount, Binding: capability.BindNone, Liveness: capability.LiveCredential}}, team)
				}
				if err := selectTeam("b"); err != nil {
					t.Fatal(err)
				}
				p, err := s.LookupCapability(t.Context(), issued.Capability.Digest)
				if err != nil || p.Capability.Team != "a" || !slices.Contains(p.Capability.Grants, capability.DataRead) {
					t.Errorf("switching to reader team changed authenticated data authority: %+v, %v", p.Capability, err)
				}
				if err := (&Tenant{s: s, team: "a"}).UpdateMembership(t.Context(), owner, MembershipUpdate{AccountID: "member"}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.LookupCapability(t.Context(), issued.Capability.Digest); err == nil {
					t.Error("removed member retained data credential")
				}
				rewriteSchemaExec(t, s.db, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','editor',1)`)
				raw, _, _, err := s.CreateAccountSession(t.Context(), Account{ID: "member"}, "a", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.LookupSession(t.Context(), raw); err != nil {
					t.Fatal(err)
				}
				if old, err := s.ResolveAuthority(t.Context(), member.Authority); err != nil || old.Capability.Team != "b" {
					t.Fatalf("old session must remain live in the other team: %+v, %v", old, err)
				}
				if _, err := s.LookupCapability(t.Context(), issued.Capability.Digest); err == nil {
					t.Fatal("fresh-session rejoin revived old delegated data credential")
				}
				if err := selectTeam("a"); err == nil {
					t.Fatal("old session selected rejoined team")
				}
			})
		}
	}
}
