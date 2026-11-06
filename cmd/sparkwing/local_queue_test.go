package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
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

func TestLocalQueueRevokesAfterHostsJoinWithCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	joined, revoked := false, false
	options := localQueueTestClient(t, func(ctx context.Context) {
		if !joined || ctx.Err() != nil {
			t.Fatalf("revocation before host cleanup or with cancelled context: joined=%t err=%v", joined, ctx.Err())
		}
		revoked = true
	})
	loop := &runners.Loop{
		Config:       runners.LoopConfig{Version: "development", MaxConcurrent: 1, MaxClaims: 1},
		CheckVersion: func(context.Context, string) error { return nil },
		Host: queueHostFunc(func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
			cancel()
			joined = true
			return ctx.Err()
		}),
	}
	if err := runLocalQueue(ctx, options, loop); !errors.Is(err, context.Canceled) {
		t.Fatalf("queue shutdown = %v", err)
	}
	if !revoked {
		t.Fatal("queue returned without revoking its credential")
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
		if !errors.As(err, &refusal) || refusal.Code != apierr.CapabilityUnsupported {
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
