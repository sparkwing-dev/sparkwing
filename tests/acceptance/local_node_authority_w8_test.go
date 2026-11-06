package acceptance_test

import (
	"crypto/sha256"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestLocalNodeAuthorityW8SurvivesStart(t *testing.T) {
	s, unit := nativeStoreAwardW8(t, capability.UnitNode)
	ctx := t.Context()
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256([]byte(unit.Capability))}
	initial, err := s.AsOperator().ReadAuthentication(ctx, lookup, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	p := initial.Authorities[0].Principal
	route, ok := capability.LookupRoute("GET", "/api/v1/runs/{run}/local-host/nodes/{job}/data-authority")
	if !ok {
		t.Fatal("missing node data-authority route")
	}
	auth := capability.NewAuthenticator(s.AsOperator())
	if _, err := auth.Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: unit.Capability}, route); err != nil {
		t.Fatal("initial claim:", err)
	}
	admitStoredUnitW8(t, s, unit)
	attempt := int64(0)
	applied, err := tenant.TransitionNode(ctx, store.StartNode{Ref: runspec.NodeRef{RunID: runspec.RunID(unit.RunID), NodeID: runspec.NodeID(unit.JobID)}, Actor: runstate.Actor{Kind: runstate.ActorHolder, Authority: p.Authority, CapabilityPrefix: p.Capability.Prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(p.Capability.Binding.RunID), NodeID: runspec.NodeID(p.Capability.Binding.NodeID), Holder: p.Capability.Binding.Holder, Generation: runspec.ClaimGeneration(p.Capability.Binding.Generation)}}, SourceAttempt: (*runspec.AttemptOrdinal)(&attempt)})
	if err != nil {
		t.Fatal("start:", err)
	}
	snapshot, err := s.AsOperator().ReadAuthentication(ctx, lookup, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := auth.Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: unit.Capability}, route)
	if err != nil || current.Principal.Capability.Binding.Attempt != int64(applied.Attempt) || snapshot.Authorities[0].Principal.Capability.Binding.Attempt != 0 || snapshot.Binding.Current.Attempt != int64(applied.Attempt) {
		t.Fatalf("started claim lost its immutable award or current attempt: %+v: %v", current.Principal.Capability.Binding, err)
	}
}

func admitStoredUnitW8(t *testing.T, s *store.Store, unit controllerwire.ClaimedUnit) {
	t.Helper()
	p, err := s.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(unit.Capability)))
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := s.ForTeam(t.Context(), store.Team(p.Capability.Team))
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: p.Authority, CapabilityPrefix: p.Capability.Prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(unit.RunID), NodeID: runspec.NodeID(unit.JobID), Holder: p.Capability.Binding.Holder, Generation: runspec.ClaimGeneration(p.Capability.Binding.Generation)}}
	result, err := tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: unit.RunID, NodeID: unit.JobID, Generation: p.Capability.Binding.Generation}, actor)
	if err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("job admission: %+v: %v", result, err)
	}
}
