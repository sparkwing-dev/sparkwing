package store

import (
	"crypto/sha256"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestOperatorTeamAdminOriginCannotMintIntoExistingTeam(t *testing.T) {
	s := authorityStore(t)
	spec := bootstrapSpec(sha256.Sum256([]byte(t.Name())))
	if _, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	operator, err := s.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(spec.Seed)))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.AsOperator().CreateOperatorTeam(t.Context(), capability.Authorized{Principal: operator}, "operator-team", "Operator team")
	if err != nil {
		t.Fatal(err)
	}
	property := func(subject, team string) bool {
		request := capability.ProvisionSpec{Kind: capability.ProvisionTeamAdmin, Authority: &operator.Authority, Team: team, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: subject}, Grants: issued.Capability.Grants, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: issued.Capability.ExpiresAt}
		got, err := s.AsOperator().ProvisionCapability(t.Context(), request)
		return err != nil && got.Raw == ""
	}
	if !property(issued.Capability.Subject.ID, issued.Capability.Team) {
		t.Fatal("origin reused an existing team")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
}

// The first administrator of an operator-created team holds team.admin like any
// other admin token, so it has the same 30-day ceiling. Rotation keeps that expiry;
// the handoff is inviting a real owner before it ends.
func TestOperatorTeamAdminExpiresLikeOtherAdminTokens(t *testing.T) {
	s := authorityStore(t)
	spec := bootstrapSpec(sha256.Sum256([]byte(t.Name())))
	if _, err := s.AsOperator().EnsureBootstrapCapability(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	operator, err := s.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(spec.Seed)))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.AsOperator().CreateOperatorTeam(t.Context(), capability.Authorized{Principal: operator}, "expiring-team", "Expiring team")
	if err != nil {
		t.Fatal(err)
	}
	if want := s.now().Add(adminTokenLifetime); issued.Capability.ExpiresAt == nil || !issued.Capability.ExpiresAt.Equal(want) {
		t.Fatalf("operator team admin expiry = %v, want %v", issued.Capability.ExpiresAt, want)
	}
}
