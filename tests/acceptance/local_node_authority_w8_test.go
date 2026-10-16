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
	route, ok := capability.LookupRoute("GET", "/api/v1/runs/{run}/local-host/nodes/{job}/data-authority")
	if !ok {
		t.Fatal("missing node data-authority route")
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

func FuzzEmbeddedServiceBearerW9KeepsAwardAndLifetime(f *testing.F) {
	f.Add("forged", false)
	f.Add("", true)
	f.Fuzz(func(t *testing.T, replacement string, owner bool) {
		kind := capability.UnitNode
		if owner {
			kind = capability.UnitTrigger
		}
		s, unit := nativeStoreAwardW8(t, kind)
		raw := unit.Capability
		finished := errors.New("services bound")
		ctx, cancelHost := context.WithCancel(t.Context())
		defer cancelHost()
		var retained func(context.Context) (string, error)
		finalized := false
		host := runners.StoreHosting{Store: s, Bind: func(ctx context.Context, binding runners.StoreBinding) (runners.NativeServices, error) {
			retained = binding.Bearer
			if retained == nil {
				t.Fatal("embedded services have no awarded credential transport")
			}
			unit.Capability = replacement
			actual, err := retained(ctx)
			if err != nil || actual != raw || sha256.Sum256([]byte(actual)) != binding.Principal.Capability.Digest {
				t.Fatal("embedded services received a substituted award")
			}
			cancelled, cancelOperation := context.WithCancel(ctx)
			cancelOperation()
			if actual, err := retained(cancelled); actual != "" || !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled service operation retained usable authority")
			}
			cancelHost()
			return runners.NativeServices{Close: func() error {
				actual, err := retained(context.WithoutCancel(ctx))
				if actual != raw || err != nil {
					t.Fatal("cancelled hosting scope lost authority before joined log cleanup")
				}
				finalized = true
				return nil
			}}, finished
		}}
		if err := host.Host(ctx, unit); !errors.Is(err, finished) || !finalized {
			t.Fatal(err)
		}
		if actual, err := retained(t.Context()); actual != "" || !errors.Is(err, context.Canceled) {
			t.Fatal("service authority outlived its hosting scope")
		}
	})
}
