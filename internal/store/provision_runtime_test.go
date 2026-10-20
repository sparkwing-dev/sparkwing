package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestProvisionMemberCurrentScopesProperty(t *testing.T) {
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
			spec := capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &member.Authority, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Team: "a", Binding: capability.Binding{Kind: capability.BindingNone}, Grants: requested}
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

func TestProvisionedMemberRootUsesMembershipWithoutSessionAncestry(t *testing.T) {
	for _, expiry := range []string{"none", "beyond_session"} {
		t.Run(expiry, func(t *testing.T) {
			s := authorityStore(t)
			member := authorityMember(t, s, "member", RoleEditor)
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET expires_at=? WHERE hash=?`, s.now().Add(time.Hour).UnixNano(), member.Authority.ID)
			spec := capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &member.Authority, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Team: "a", Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite}}
			if expiry == "beyond_session" {
				at := s.now().Add(24 * time.Hour)
				spec.ExpiresAt = &at
			}
			root, err := s.AsOperator().ProvisionCapability(t.Context(), spec)
			if err != nil || root.Raw == "" || root.Capability.Parent != nil || root.Capability.Subject != spec.Subject || !reflect.DeepEqual(root.Capability.ExpiresAt, spec.ExpiresAt) {
				t.Fatalf("provisioned root: %+v, %v", root.Capability, err)
			}
			rewriteSchemaExec(t, s.db, `UPDATE sessions SET revoked_at=1 WHERE hash=?`, member.Authority.ID)
			if _, err := s.AsOperator().LookupCapability(t.Context(), root.Capability.Digest); err != nil {
				t.Fatalf("session revoke ended independent root: %v", err)
			}
			rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
			if _, err := s.AsOperator().LookupCapability(t.Context(), root.Capability.Digest); err == nil {
				t.Fatal("removed membership retained root authority")
			}
		})
	}
}

func TestRootProvisioningRechecksAuthorizerAndScope(t *testing.T) {
	s := authorityStore(t)
	member := authorityMember(t, s, "member", RoleEditor)
	for _, problem := range []string{"subject", "team", "grants", "metered", "pools", "expired", "revoked"} {
		t.Run(problem, func(t *testing.T) {
			spec := capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &member.Authority, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Team: "a", Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}}
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
				issued, err = s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Delegate, Provision: &spec})
				return err
			})
			if err == nil || issued.Raw != "" {
				t.Fatal("untrusted root provisioning succeeded")
			}
		})
	}
}
