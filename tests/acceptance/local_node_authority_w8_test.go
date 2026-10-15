package acceptance_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
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
	current, err := auth.Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: unit.Capability}, route)
	if err != nil || current.Principal.Capability.Binding.Attempt != applied.Attempt || snapshot.Authorities[0].Principal.Capability.Binding.Attempt != 0 || snapshot.Binding.Current.Attempt != applied.Attempt {
		t.Fatalf("started claim lost its immutable award or current attempt: %+v: %v", current.Principal.Capability.Binding, err)
	}
}

func TestStoreHostingW8RefreshesNodeAwardAfterStart(t *testing.T) {
	s, unit := nativeStoreAwardW8(t, capability.UnitNode)
	finished := errors.New("binding finished")
	refreshed, closed := false, false
	host := runners.StoreHosting{Store: s, Bind: func(ctx context.Context, binding runners.StoreBinding) (runners.NativeServices, error) {
		p := binding.Principal
		attempt := int64(0)
		applied, err := binding.Tenant.TransitionNode(ctx, store.NodeTransition{
			Ref: store.NodeRef{RunID: unit.RunID, NodeID: unit.JobID}, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: &attempt,
			Actor: store.Actor{Kind: store.ActorHolder, Authority: p.Authority, CapabilityPrefix: p.Capability.Prefix, Holder: p.Capability.Binding.Holder, Generation: p.Capability.Binding.Generation},
		})
		if err != nil {
			return runners.NativeServices{}, err
		}
		current, err := binding.Refresh(ctx)
		if err != nil || current.Authority != p.Authority || current.Capability.Binding.Attempt != applied.Attempt || unit.Binding.Attempt != 0 {
			t.Fatalf("started host lost its authenticated award: %+v: %v", current.Capability.Binding, err)
		}
		refreshed = true
		return runners.NativeServices{Close: func() error { closed = true; return nil }}, finished
	}}
	if err := host.Host(t.Context(), unit); !errors.Is(err, finished) || !refreshed || !closed {
		t.Fatalf("refreshed=%v closed=%v: %v", refreshed, closed, err)
	}
}
