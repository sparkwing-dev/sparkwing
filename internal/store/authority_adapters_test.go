package store

import (
	"crypto/sha256"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestAuthorityAdaptersResolveLiveAncestryWithoutUnitAuthorization(t *testing.T) {
	tenant, _, _ := stateFixture(t)
	s := tenant.s
	member := authorityMember(t, s, "member", RoleEditor)
	child := authorityRoot("child", "a")
	child.Parent = &member.Authority
	child.Grants = []capability.Grant{capability.RunsRead, capability.RunsWrite, capability.TeamAdmin}
	child.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "former-holder", Generation: 1}
	authorityPersist(t, s, child)
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix}
	resolved, err := s.AsOperator().ResolveAuthority(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	lookedUp, err := s.AsOperator().LookupCapability(t.Context(), child.Digest)
	if err != nil || !reflect.DeepEqual(resolved, lookedUp) {
		t.Fatalf("digest lookup differs from reference lookup: %v", err)
	}
	if resolved.Authority != ref || resolved.Class != capability.CredentialBearer || resolved.Capability.Binding != child.Binding || !slices.Contains(resolved.Capability.Grants, capability.RunsWrite) || slices.Contains(resolved.Capability.Grants, capability.TeamAdmin) {
		t.Fatalf("resolved authority = %+v", resolved)
	}
	session, err := s.AsOperator().ResolveAuthority(t.Context(), member.Authority)
	if err != nil || session.Class != capability.CredentialSession || session.Capability.Subject.ID != "member" {
		t.Fatalf("resolved session = %+v, %v", session, err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='member'`)
	lookedUp, err = s.AsOperator().LookupCapability(t.Context(), child.Digest)
	if err != nil || slices.Contains(lookedUp.Capability.Grants, capability.RunsWrite) || !slices.Contains(lookedUp.Capability.Grants, capability.RunsRead) {
		t.Fatalf("membership narrowing = %+v, %v", lookedUp, err)
	}
}

func TestAuthorityAdaptersRefuseUnavailableAncestry(t *testing.T) {
	for _, problem := range []string{"missing", "revoked", "expired", "membership_removed", "forged_digest"} {
		t.Run(problem, func(t *testing.T) {
			s := authorityStore(t)
			member := authorityMember(t, s, "member", RoleEditor)
			parent := authorityRoot("parent", "a")
			parent.Parent = &member.Authority
			if problem == "expired" {
				ended := time.Unix(2, 0).UTC()
				parent.ExpiresAt = &ended
			}
			if problem == "revoked" {
				ended := time.Unix(1, 0).UTC()
				parent.RevokedAt = &ended
			}
			if problem != "missing" {
				authorityPersist(t, s, parent)
			}
			child := authorityRoot("child", "a")
			child.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Prefix}
			authorityPersist(t, s, child)
			if problem == "membership_removed" {
				rewriteSchemaExec(t, s.db, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
			}
			digest := child.Digest
			if problem == "forged_digest" {
				digest[0] ^= 1
			} else {
				principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: child.Prefix})
				assertUnavailableAuthority(t, principal, err)
			}
			principal, err := s.AsOperator().LookupCapability(t.Context(), digest)
			assertUnavailableAuthority(t, principal, err)
		})
	}
}

func assertUnavailableAuthority(t *testing.T, principal capability.Principal, err error) {
	t.Helper()
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != apierr.Unauthenticated || !reflect.DeepEqual(principal, capability.Principal{}) {
		t.Fatalf("unavailable authority returned %+v, %v", principal, err)
	}
}

func adapterBootstrap(t *testing.T) (*Store, capability.Issued) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	fundUnitClaim(t, s)
	rewriteSchemaExec(t, s.db, "DELETE FROM capabilities WHERE prefix='runner'")
	root := authorityRoot("launcher", "")
	root.Subject.Kind = capability.SubjectService
	root.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, s, root)
	rewriteSchemaExec(t, s.db, "UPDATE runs SET dispatch='controller'")
	demand.CapabilityPrefix = root.Prefix
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AsOperator().LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	return s, capability.Issued{Raw: string(claim.Bearer), Capability: p.Capability}
}

func TestExchangeCapabilityCommitsOnceAndPreservesTheAward(t *testing.T) {
	s, bootstrap := adapterBootstrap(t)
	issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: bootstrap.Capability.Prefix})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Raw == "" || issued.Capability.Digest != sha256.Sum256([]byte(issued.Raw)) || issued.Capability.Parent.ID != "launcher" || issued.Capability.Delivery != capability.DeliveryDirect || !reflect.DeepEqual(issued.Capability.Billing, bootstrap.Capability.Billing) {
		t.Fatal("exchanged capability lost its secret, parent, delivery or billing identity")
	}
	principal, err := s.AsOperator().LookupCapability(t.Context(), issued.Capability.Digest)
	if err != nil || principal.Capability.Prefix != issued.Capability.Prefix {
		t.Fatalf("committed exchange lookup = %+v, %v", principal, err)
	}
	var prefix string
	if err := s.db.QueryRow(`SELECT capability_prefix FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&prefix); err != nil || prefix != issued.Capability.Prefix {
		t.Fatalf("exchanged unit fence = %q, %v", prefix, err)
	}
	principal, err = s.AsOperator().LookupCapability(t.Context(), bootstrap.Capability.Digest)
	assertUnavailableAuthority(t, principal, err)
	again, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: bootstrap.Capability.Prefix})
	if err == nil || !reflect.DeepEqual(again, capability.Issued{}) {
		t.Fatal("bootstrap exchanged more than once")
	}
	rewriteSchemaCount(t, s.db, "capabilities", 3)
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
}

func TestExchangeCapabilityRollbackReturnsNoBearer(t *testing.T) {
	s, bootstrap := adapterBootstrap(t)
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_exchange BEFORE UPDATE OF capability_prefix ON nodes BEGIN SELECT RAISE(ABORT,'unit write refused'); END`)
	issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: bootstrap.Capability.Prefix})
	if err == nil || !reflect.DeepEqual(issued, capability.Issued{}) {
		t.Fatal("failed exchange returned a capability")
	}
	rewriteSchemaCount(t, s.db, "capabilities", 2)
	if _, err := s.AsOperator().LookupCapability(t.Context(), bootstrap.Capability.Digest); err != nil {
		t.Fatalf("rollback consumed bootstrap: %v", err)
	}
	rewriteSchemaExec(t, s.db, `DROP TRIGGER refuse_exchange`)
	if _, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: bootstrap.Capability.Prefix}); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}
