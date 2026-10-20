package teststore_test

import (
	"crypto/sha256"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestProvisionCredentialUsesCurrentIssuer(t *testing.T) {
	s, err := teststore.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, spec := range []capability.ProvisionSpec{
		{Kind: capability.ProvisionBootstrap},
		{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"}, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone}},
		{Kind: capability.ProvisionMachine, Repositories: []string{"github.com/*/*"}, Team: string(store.DefaultTeam), RegistrationID: "runner:default:runner", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}},
		{Kind: capability.ProvisionService, RegistrationID: "service:launcher", Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Grants: []capability.Grant{capability.ClaimsLaunch}, Binding: capability.Binding{Kind: capability.BindingNone}},
	} {
		t.Run(string(spec.Kind), func(t *testing.T) {
			raw, c, err := teststore.ProvisionCredential(t.Context(), s, spec)
			if err != nil || raw == "" || c == nil {
				t.Fatalf("fixture issuance: %v", err)
			}
			p, err := s.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
			if err != nil || p.Capability.Prefix != c.Prefix {
				t.Fatalf("fixture authentication: %v", err)
			}
		})
	}
}
