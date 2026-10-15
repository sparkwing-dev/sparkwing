//go:build rewrite_integration

package localcontroller

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestLocalClaimantW8UsesPersistedPeerAuthority(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	claimant, err := NewClaimant(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := claimant.Close(); err != nil {
			t.Error(err)
		}
	})
	raw, err := claimant.Bearer(t.Context())
	if err != nil {
		t.Fatal("verified peer cannot obtain local queue authority:", err)
	}
	route, ok := capability.LookupRoute("POST", "/api/v1/runners/claim")
	if !ok {
		t.Fatal("missing shared claim route")
	}
	target := capability.Target{Team: string(store.DefaultTeam), ClaimKinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}}
	verified, err := capability.NewAuthenticator(s).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: raw, Evidence: capability.RequestEvidence{Target: target}}, route)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capability.Authorize(t.Context(), s, route, verified, target); err != nil {
		t.Fatal(err)
	}
	if verified.Principal.Authority.ID == "" || verified.Principal.Capability.Subject != (capability.Subject{Kind: capability.SubjectLocal, ID: claimant.subject}) {
		t.Fatal("local claimant lost its persisted verified account")
	}
}
