package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func localQueueTestClient(t *testing.T, revoked func(context.Context)) client.EngineOptions {
	t.Helper()
	return client.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: queueHostTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/tokens":
			if r.Header.Get("Authorization") != "" {
				t.Fatal("claimant issued its own credential")
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(controllerwire.CreatedToken{Capability: "local-claimant", Prefix: "local-prefix", Team: "default", Grants: []string{"triggers.claim", "nodes.claim"}})
		case "POST /api/v1/runners/claim":
			if r.Header.Get("Authorization") != "Bearer local-claimant" {
				t.Fatal("claim lost its issued authority")
			}
			unit := localTestAward(t)
			_ = json.NewEncoder(w).Encode(controllerwire.ClaimResponse{Claim: &unit})
		case "DELETE /api/v1/tokens/local-prefix":
			if r.Header.Get("Authorization") != "" {
				t.Fatal("revocation lost peer authority")
			}
			if revoked != nil {
				revoked(r.Context())
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected queue request: %s %s", r.Method, r.URL.Path)
		}
		return w.Result(), nil
	})}}
}

type queueHostFunc func(context.Context, controllerwire.ClaimedUnit) error

func (f queueHostFunc) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	return f(ctx, unit)
}

type queueLifecycleFixture struct {
	instance localhostwire.LocalRunnerInstance
	activate func() error
	watch    func(context.Context) (localhostwire.LocalRunnerInstance, error)
	stop     func()
	drain    func(context.Context) error
}

func (f *queueLifecycleFixture) Drain(ctx context.Context, _ string) error {
	if f.drain != nil {
		return f.drain(ctx)
	}
	return nil
}

func (f *queueLifecycleFixture) Register(_ context.Context, instance localhostwire.LocalRunnerInstance) (localhostwire.LocalRunnerInstance, error) {
	f.instance = instance
	f.instance.Machine, f.instance.State = "machine", "registered"
	return f.instance, nil
}

func (f *queueLifecycleFixture) Activate(context.Context, string) error {
	if f.activate != nil {
		return f.activate()
	}
	f.instance.State = "active"
	return nil
}

func (f *queueLifecycleFixture) Watch(ctx context.Context, _, _ string) (localhostwire.LocalRunnerInstance, error) {
	if f.watch != nil {
		return f.watch(ctx)
	}
	<-ctx.Done()
	return localhostwire.LocalRunnerInstance{}, ctx.Err()
}

func (f *queueLifecycleFixture) Stop(context.Context, string) error {
	if f.stop != nil {
		f.stop()
	}
	return nil
}

func TestLocalQueueRevokesAfterHostsJoinWithCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	joined, stopped, revoked := false, false, false
	options := localQueueTestClient(t, func(ctx context.Context) {
		if !joined || !stopped || ctx.Err() != nil {
			t.Fatalf("revocation before host cleanup or with cancelled context: joined=%t stopped=%t err=%v", joined, stopped, ctx.Err())
		}
		revoked = true
	})
	lifecycle := &queueLifecycleFixture{stop: func() {
		if !joined {
			t.Fatal("runner stopped before its host joined")
		}
		stopped = true
	}}
	loop := &runners.Loop{
		Config:       runners.LoopConfig{Version: "development", MaxConcurrent: 1, MaxClaims: 1},
		CheckVersion: func(context.Context, string) error { return nil },
		Host: queueHostFunc(func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
			cancel()
			joined = true
			return ctx.Err()
		}),
	}
	if err := runLocalQueue(ctx, "test-instance", options, loop, lifecycle); !errors.Is(err, context.Canceled) {
		t.Fatalf("queue shutdown = %v", err)
	}
	if !revoked {
		t.Fatal("queue returned without revoking its credential")
	}
}

func TestLocalQueueDrainWatchJoinsSaturatedHosts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, finish, drain, draining := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		joined, stopped := false, false
		options := localQueueTestClient(t, func(context.Context) {
			if !joined || !stopped {
				t.Error("revoked before the draining host and lifecycle joined")
			}
		})
		lifecycle := &queueLifecycleFixture{
			watch: func(ctx context.Context) (localhostwire.LocalRunnerInstance, error) {
				select {
				case <-drain:
					return localhostwire.LocalRunnerInstance{State: "draining"}, nil
				case <-ctx.Done():
					return localhostwire.LocalRunnerInstance{}, ctx.Err()
				}
			},
			stop:  func() { stopped = true },
			drain: func(context.Context) error { close(draining); return nil },
		}
		loop := &runners.Loop{
			Config:       runners.LoopConfig{Version: "development", MaxConcurrent: 1, Kinds: []capability.UnitKind{capability.UnitTrigger}},
			CheckVersion: func(context.Context, string) error { return nil },
			Host: queueHostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
				close(started)
				<-finish
				if ctx.Err() != nil {
					t.Errorf("drain cancelled a held host: %v", ctx.Err())
				}
				joined = true
				return nil
			}),
		}
		done := make(chan error, 1)
		go func() { done <- runLocalQueue(t.Context(), "test-instance", options, loop, lifecycle) }()
		<-started
		close(drain)
		<-draining
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("runner returned before held host: %v", err)
		default:
		}
		close(finish)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !stopped {
			t.Fatal("runner did not acknowledge its joined host")
		}
	})
}

func TestLocalQueueActivationFailureStopsAndRevokesWithoutClaim(t *testing.T) {
	failure := errors.New("activation failed")
	stopped, revoked := false, false
	options := localQueueTestClient(t, func(context.Context) { revoked = true })
	lifecycle := &queueLifecycleFixture{activate: func() error { return failure }, stop: func() { stopped = true }}
	loop := &runners.Loop{Config: runners.LoopConfig{Version: "development"}, CheckVersion: func(context.Context, string) error { return nil }}
	if err := runLocalQueue(t.Context(), "test-instance", options, loop, lifecycle); !errors.Is(err, failure) {
		t.Fatalf("activation: %v", err)
	}
	if !stopped || !revoked {
		t.Fatalf("failed activation leaked identity or authority: stopped=%t revoked=%t", stopped, revoked)
	}
}

func TestLocalQueuePeerCredentialOnlyClaimsAndRevokesThroughAPI(t *testing.T) {
	options := productionLocalPolicyClient(t)
	tokens, err := client.NewTokenManager(options, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []controllerwire.TokenCreate{
		{Scopes: []capability.Grant{}},
		{Scopes: []capability.Grant{capability.NodesClaim}},
		{Scopes: []capability.Grant{capability.TriggersClaim}},
		{Scopes: []capability.Grant{capability.NodesClaim, capability.TriggersClaim, capability.RunsRead}},
		{Scopes: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, AllowTeamAdmin: true},
	} {
		_, err := tokens.CreateToken(t.Context(), input)
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Code != hosterr.CapabilityUnsupported {
			t.Fatalf("local peer non-queue credential refusal: %+v: %v", input, err)
		}
	}
	issued, err := tokens.CreateToken(t.Context(), controllerwire.TokenCreate{Scopes: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}})
	if err != nil {
		t.Fatal(err)
	}
	options.Bearer = func(context.Context) (string, error) { return issued.Token, nil }
	claimant, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	request := controllerwire.ClaimRequest{RunnerVersion: installedVersion(), Kinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}}
	response, err := claimant.Claim(t.Context(), request)
	if err != nil || response.Claim != nil {
		t.Fatalf("queue credential could not poll: %+v: %v", response, err)
	}
	if _, err := tokens.RevokeToken(t.Context(), issued.Metadata.Prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := claimant.Claim(t.Context(), request); err == nil {
		t.Fatal("revoked queue credential still claims work")
	}
}

func TestLocalQueuePeerCredentialExpires(t *testing.T) {
	var offset atomic.Int64
	options := productionLocalPolicyClient(t, teststore.WithClock(func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }))
	tokens, err := client.NewTokenManager(options, false)
	if err != nil {
		t.Fatal(err)
	}
	scopes := []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
	beyond := int64((controller.LocalClaimantLifetime + time.Hour) / time.Second)
	_, err = tokens.CreateToken(t.Context(), controllerwire.TokenCreate{Scopes: scopes, TTLSecs: &beyond})
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != apierr.BadRequest {
		t.Fatalf("local claimant outliving its bound: %v", err)
	}
	issued, err := tokens.CreateToken(t.Context(), controllerwire.TokenCreate{Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	if expires := issued.Metadata.ExpiresAt; expires == nil || time.Unix(*expires, 0).After(time.Now().Add(controller.LocalClaimantLifetime)) {
		t.Fatalf("default local claimant expiry: %v", expires)
	}
	options.Bearer = func(context.Context) (string, error) { return issued.Token, nil }
	claimant, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	request := controllerwire.ClaimRequest{RunnerVersion: installedVersion(), Kinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}}
	if _, err := claimant.Claim(t.Context(), request); err != nil {
		t.Fatalf("fresh queue credential could not poll: %v", err)
	}
	offset.Store(int64(controller.LocalClaimantLifetime + time.Minute))
	if _, err := claimant.Claim(t.Context(), request); !errors.As(err, &refusal) || refusal.Code != hosterr.Unauthenticated {
		t.Fatalf("expired queue credential: %v", err)
	}
}

func TestLocalQueueRollsBeforeItsCredentialExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		drained := make(chan struct{})
		lifecycle := &queueLifecycleFixture{drain: func(context.Context) error { close(drained); return nil }}
		loop := &runners.Loop{
			Config:       runners.LoopConfig{Version: "development", MaxConcurrent: 1, Kinds: []capability.UnitKind{capability.UnitTrigger}},
			CheckVersion: func(context.Context, string) error { return nil },
			Host:         queueHostFunc(func(context.Context, controllerwire.ClaimedUnit) error { <-drained; return nil }),
		}
		options := localQueueTestClient(t, nil)
		start := time.Now()
		if err := runLocalQueue(t.Context(), "test-instance", options, loop, lifecycle); err != nil {
			t.Fatal(err)
		}
		<-drained
		if elapsed := time.Since(start); elapsed < localRunnerRollAfter || elapsed >= controller.LocalClaimantLifetime {
			t.Fatalf("runner rolled after %s", elapsed)
		}
	})
}
