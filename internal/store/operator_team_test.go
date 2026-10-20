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
	if _, err := s.EnsureBootstrapCapability(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	operator, err := s.LookupCapability(t.Context(), sha256.Sum256([]byte(spec.Seed)))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.CreateOperatorTeam(t.Context(), capability.Authorized{Principal: operator}, "operator-team", "Operator team")
	if err != nil {
		t.Fatal(err)
	}
	property := func(subject, team string) bool {
		request := capability.ProvisionSpec{Kind: capability.ProvisionTeamAdmin, Authority: &operator.Authority, Team: team, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: subject}, Grants: issued.Capability.Grants, Binding: capability.Binding{Kind: capability.BindingNone}}
		got, err := s.ProvisionCapability(t.Context(), request)
		return err != nil && got.Raw == ""
	}
	if !property(issued.Capability.Subject.ID, issued.Capability.Team) {
		t.Fatal("origin reused an existing team")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
}
