package store

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type delegationValidationFunc func(capability.Principal, capability.DelegateSpec, time.Time) error

func (f delegationValidationFunc) ValidateDelegation(parent capability.Principal, child capability.DelegateSpec, now time.Time) error {
	return f(parent, child, now)
}

func TestDelegationRetainsAwardIdentityWithoutAnotherBillingAward(t *testing.T) {
	s, demand := unitClaimFixture(t)
	claim, err := s.ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	s.delegation = delegationValidationFunc(func(capability.Principal, capability.DelegateSpec, time.Time) error { return nil })
	child, err := s.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: p.Authority, Audience: capability.Controller, Binding: p.Capability.Binding, Repositories: p.Capability.Repositories, Grants: []capability.Grant{capability.NodeReport}, ExpiresAt: p.Capability.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	c := child.Capability
	if c.Unit != p.Capability.Unit || c.UnitID != p.Capability.UnitID || c.Purpose != p.Capability.Purpose || c.Delivery != p.Capability.Delivery || !reflect.DeepEqual(c.Billing, p.Capability.Billing) {
		t.Fatal("narrowed child lost the immutable award identity")
	}
	target := capability.Target{Team: "a", RunID: "run", ClaimID: c.UnitID, Unit: c.Unit, UnitGeneration: c.Binding.Generation, UnitHolder: c.Binding.Holder}
	if err := s.CheckBinding(t.Context(), capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}}, capability.BindOwnClaim, target); err != nil {
		t.Fatal(err)
	}
	target.UnitGeneration++
	if err := s.CheckBinding(t.Context(), capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}}, capability.BindOwnClaim, target); err == nil {
		t.Fatal("narrowed child replaced its generation")
	}
	rewriteSchemaCount(t, s.db, "billing_awards", 1)
}

func TestDelegationPersistsOnlyTheValidatedNarrowing(t *testing.T) {
	s := authorityStore(t)
	root := authorityRoot("root", "a")
	root.Grants = []capability.Grant{capability.RunsRead, capability.RunsWrite}
	root.ReservedPools = []string{"reserved"}
	authorityPersist(t, s, root)
	spec := capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}}
	called := 0
	s.delegation = delegationValidationFunc(func(parent capability.Principal, child capability.DelegateSpec, now time.Time) error {
		called++
		if parent.Authority != spec.Parent || parent.Capability.Subject != root.Subject || !slices.Equal(child.Grants, spec.Grants) || now.IsZero() {
			t.Fatal("delegation did not validate current parent and requested child")
		}
		return nil
	})
	issued, err := s.DelegateCapability(t.Context(), spec)
	if err != nil || called != 1 || issued.Raw == "" || issued.Capability.Parent == nil || *issued.Capability.Parent != spec.Parent || issued.Capability.Subject != root.Subject || !slices.Equal(issued.Capability.Grants, spec.Grants) || !slices.Equal(issued.Capability.ReservedPools, root.ReservedPools) || issued.Capability.Billing != nil || issued.Capability.Unit != "" {
		t.Fatalf("delegation: %+v, %v", issued.Capability, err)
	}
	principal, err := s.LookupCapability(t.Context(), issued.Capability.Digest)
	if err != nil || principal.Authority.ID != issued.Capability.Prefix {
		t.Fatalf("committed delegate: %+v, %v", principal.Authority, err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='root'`)
	if _, err := s.LookupCapability(t.Context(), issued.Capability.Digest); err == nil {
		t.Fatal("ordinary delegation detached from revoked parent")
	}
}

func TestDelegationReturnsNoBearerAfterValidationOrPersistenceFailure(t *testing.T) {
	for _, problem := range []string{"validator", "expired_during_validation", "persistence"} {
		t.Run(problem, func(t *testing.T) {
			s := authorityStore(t)
			now := time.Unix(1700000000, 0).UTC()
			s.clock = func() time.Time { return now }
			root := authorityRoot("root", "a")
			root.Grants = []capability.Grant{capability.RunsRead}
			until := now.Add(time.Minute)
			root.ExpiresAt = &until
			authorityPersist(t, s, root)
			s.delegation = delegationValidationFunc(func(capability.Principal, capability.DelegateSpec, time.Time) error {
				if problem == "validator" {
					return errors.New("delegation refused")
				}
				if problem == "expired_during_validation" {
					now = until
				}
				return nil
			})
			if problem == "persistence" {
				rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_delegate BEFORE INSERT ON capabilities BEGIN SELECT RAISE(ABORT,'storage refused'); END`)
			}
			issued, err := s.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}, ExpiresAt: &until})
			if err == nil || issued.Raw != "" || issued.Capability.Prefix != "" {
				t.Fatalf("failed delegation exposed bearer: %v", err)
			}
			rewriteSchemaCount(t, s.db, "capabilities", 1)
		})
	}
}
