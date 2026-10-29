package store

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestProvisionMemberCurrentScopesProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.6s measured in the short class; the full suite runs it")
	}
	random := rand.New(rand.NewPCG(127, 901))
	grants := []capability.Grant{capability.RunsRead, capability.LogsRead, capability.RunsWrite, capability.TeamAdmin, capability.NodesClaim, capability.OperatorTokens, "unknown"}
	for i := range 40 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := authorityStore(t)
			member := authorityMember(t, s, "member", RoleOwner)
			role := []Role{RoleReader, RoleEditor, RoleOwner}[random.IntN(3)]
			removed := i%7 == 0
			if removed {
				rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
			} else {
				rewriteSchemaExec(t, s.db, `UPDATE memberships SET role=? WHERE team='a' AND account_id='member'`, role)
			}
			requested := []capability.Grant{}
			for _, n := range random.Perm(len(grants))[:1+random.IntN(3)] {
				requested = append(requested, grants[n])
			}
			original := slices.Clone(requested)
			spec := capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &member.Authority, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Team: "a", Binding: capability.Binding{Kind: capability.BindingNone}, Grants: requested}
			issued, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
			permitted := []capability.Grant{capability.RunsRead, capability.LogsRead}
			if role == RoleEditor || role == RoleOwner {
				permitted = append(permitted, capability.RunsWrite)
			}
			if role == RoleOwner {
				permitted = append(permitted, capability.TeamAdmin)
			}
			allowed := !removed && !slices.ContainsFunc(requested, func(g capability.Grant) bool { return !slices.Contains(permitted, g) })
			if !slices.Equal(requested, original) {
				t.Fatal("provisioning changed requested grants")
			}
			if allowed {
				if err != nil || issued.Raw == "" || !slices.Equal(issued.Capability.Grants, requested) {
					t.Fatalf("requested subset changed: %+v, %v", issued.Capability.Grants, err)
				}
			} else {
				var count int
				if readErr := s.db.QueryRow(`SELECT COUNT(*) FROM capabilities`).Scan(&count); readErr != nil {
					t.Fatal(readErr)
				}
				if err == nil || issued.Raw != "" || count != 0 {
					t.Fatalf("refused authority persisted a credential: count=%d, error=%v", count, err)
				}
			}
		})
	}
}

func TestProvisionedMemberTokenOutlivesSessionTimeoutAndDiesOnLogout(t *testing.T) {
	for _, timeout := range []string{"idle", "absolute"} {
		t.Run(timeout, func(t *testing.T) {
			s, account, now := sessionRuntimeStore(t)
			cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			s.csrfCipher = cipher
			raw, _, session, err := s.AsOperator().CreateAccountSession(t.Context(), account, "a", 7*24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			member := runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: sessionDigest(raw)}}
			spec := capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &member.Authority, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: account.ID}, Team: "a", Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite}}
			expected := now.Add(90 * 24 * time.Hour)
			if timeout == "absolute" {
				expected = now.Add(60 * 24 * time.Hour)
				spec.ExpiresAt = &expected
			}
			token, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if token.Capability.Parent == nil || *token.Capability.Parent != member.Authority || token.Capability.ExpiresAt == nil || !token.Capability.ExpiresAt.Equal(expected) {
				t.Fatalf("member token: %+v", token.Capability)
			}
			child, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: token.Capability.Prefix}, Audience: capability.Controller, Binding: spec.Binding, Grants: []capability.Grant{capability.RunsRead}})
			if err != nil {
				t.Fatal(err)
			}
			if timeout == "absolute" {
				created := session.CreatedAt
				for days := 6; days < 30; days += 6 {
					*now = created.Add(time.Duration(days) * 24 * time.Hour)
					session, err = s.AsOperator().LookupSessionAndRenew(t.Context(), raw, 7*24*time.Hour, 30*24*time.Hour)
					if err != nil {
						t.Fatal(err)
					}
				}
				if !session.ExpiresAt.Equal(created.Add(30 * 24 * time.Hour)) {
					t.Fatal("session never reached its absolute timeout")
				}
			}
			*now = session.ExpiresAt
			if _, err := s.AsOperator().LookupSession(t.Context(), raw); err == nil {
				t.Fatal("timed-out session authenticated")
			}
			for _, issued := range []capability.Issued{token, child} {
				if _, err := s.AsOperator().LookupCapability(t.Context(), issued.Capability.Digest); err != nil {
					t.Fatalf("session timeout expired token: %v", err)
				}
				if err := s.inTx(t.Context(), func(tx *storeTx) error {
					_, err := tx.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix})
					return err
				}); err != nil {
					t.Fatalf("transaction rejected token after session timeout: %v", err)
				}
				snapshot, err := s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Digest: issued.Capability.Digest, Audience: capability.Controller}, capability.RequestEvidence{})
				if err != nil {
					t.Fatal(err)
				}
				principal, err := capability.ValidateAuthority(snapshot.Authorities, snapshot.Now)
				if err != nil || principal.Capability.ExpiresAt == nil || !principal.Capability.ExpiresAt.Equal(expected) {
					t.Fatalf("authentication shortened reported expiry: %+v, %v", principal, err)
				}
			}
			if _, err := s.AsOperator().DeleteSession(raw); err != nil {
				t.Fatal(err)
			}
			for _, issued := range []capability.Issued{token, child} {
				if _, err := s.AsOperator().LookupCapability(t.Context(), issued.Capability.Digest); err == nil {
					t.Fatal("token survived logout")
				}
			}
		})
	}
}

func TestRootProvisioningRechecksAuthorizerAndScope(t *testing.T) {
	s := authorityStore(t)
	member := authorityMember(t, s, "member", RoleEditor)
	for _, problem := range []string{"subject", "team", "grants", "metered", "pools", "expired", "revoked"} {
		t.Run(problem, func(t *testing.T) {
			spec := capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &member.Authority, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Team: "a", Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}}
			switch problem {
			case "subject":
				spec.Subject.ID = "other"
			case "team":
				spec.Team = "b"
			case "grants":
				spec.Grants = []capability.Grant{capability.OperatorTokens}
			case "metered":
				spec.Metered = true
			case "pools":
				spec.ReservedPools = []string{"trusted"}
			case "expired":
				now, err := s.databaseNow(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				spec.ExpiresAt = &now
			case "revoked":
				rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=1 WHERE hash=?`, member.Authority.ID)
			}
			var issued capability.Issued
			err := s.inTx(t.Context(), func(tx *storeTx) error {
				var err error
				issued, err = s.issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Delegate, Provision: &spec})
				return err
			})
			if err == nil || issued.Raw != "" {
				t.Fatal("untrusted root provisioning succeeded")
			}
		})
	}
}
