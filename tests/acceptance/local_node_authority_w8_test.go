//go:build rewrite_integration

package acceptance_test

import (
	"crypto/sha256"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestLocalNodeAuthorityW8SurvivesStart(t *testing.T) {
	s, unit := nativeStoreAwardW8(t, capability.UnitNode)
	ctx := t.Context()
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256([]byte(unit.Capability))}
	initial, err := s.ReadAuthentication(ctx, lookup, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	p := initial.Authorities[0].Principal
	route, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/claim/input")
	if !ok {
		t.Fatal("missing input route")
	}
	auth := capability.NewAuthenticator(s)
	if _, err := auth.Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: unit.Capability}, route); err != nil {
		t.Fatal("initial claim:", err)
	}
	attempt := int64(0)
	applied, err := tenant.TransitionNode(ctx, store.NodeTransition{
		Ref: store.NodeRef{RunID: unit.RunID, NodeID: unit.JobID}, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: &attempt,
		Actor: store.Actor{Kind: store.ActorHolder, Authority: p.Authority, CapabilityPrefix: p.Capability.Prefix, Holder: p.Capability.Binding.Holder, Generation: p.Capability.Binding.Generation},
	})
	if err != nil {
		t.Fatal("start:", err)
	}
	snapshot, err := s.ReadAuthentication(ctx, lookup, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("applied attempt=%d credential attempt=%d live binding attempt=%d", applied.Attempt, snapshot.Authorities[0].Principal.Capability.Binding.Attempt, snapshot.Binding.Current.Attempt)
	if _, err := auth.Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: unit.Capability}, route); err != nil {
		t.Fatal("started claim cannot authenticate:", err)
	}
}
