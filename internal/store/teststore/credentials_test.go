package teststore_test

import (
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestProvisionCredentialUsesStoreClock(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	s, err := teststore.Open(filepath.Join(t.TempDir(), "store.db"), teststore.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	raw, _, err := teststore.ProvisionCredential(t.Context(), s, capability.ProvisionSpec{
		Kind: capability.ProvisionMember, Team: string(store.DefaultTeam),
		Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"},
		Grants:  []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupCapability(t.Context(), sha256.Sum256([]byte(raw))); err != nil {
		t.Fatalf("credential fixture differs from its store clock: %v", err)
	}
	snapshot, err := s.ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256([]byte(raw))}, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	parent := snapshot.Authorities[len(snapshot.Authorities)-1].Principal.Capability
	if !parent.IssuedAt.Equal(now) {
		t.Fatalf("minting session was created at %s, want %s", parent.IssuedAt, now)
	}
}

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
			p, err := s.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
			if err != nil || p.Capability.Prefix != c.Prefix {
				t.Fatalf("fixture authentication: %v", err)
			}
		})
	}
}
