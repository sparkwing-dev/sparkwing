package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
)

type queueHostTransport func(*http.Request) (*http.Response, error)

func (f queueHostTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failedQueueHost struct{ err error }

func (h failedQueueHost) Host(context.Context, controllerwire.ClaimedUnit) error { return h.err }

type queueClaim struct{ unit controllerwire.ClaimedUnit }

func (q queueClaim) Claim(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return controllerwire.ClaimResponse{Claim: &q.unit}, nil
}

func TestLocalQueueReportsHostFailureWhileDraining(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(t.TempDir(), "secrets.key"))
	home := t.TempDir()
	owned, err := teststore.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owned.Close(); err != nil {
			t.Error(err)
		}
	})
	tenant, err := owned.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	engine := &controller.EngineController{Store: owned}
	services, err := bindLocalController(t.Context(), localcontroller.Binding{Home: home, Store: owned, Tenant: tenant, Controller: engine})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Error(err)
		}
	})
	services.Runner.Loop.MaxClaims = 1
	loop, err := runners.NewStoreRunner(engine, services.Runner)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("profile cache construction failed")
	loop.Controller, loop.Host = queueClaim{localTestAward(t)}, failedQueueHost{failure}
	loop.CheckVersion = func(context.Context, string) error { return nil }
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	if err := loop.Run(t.Context()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "local run host failed") || !strings.Contains(output.String(), failure.Error()) {
		t.Fatalf("host failure stayed silent: %s", &output)
	}
}

func TestLocalQueueBindsAuthenticatedStorageBeforeOpeningProcessTransport(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	owned, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owned.Close(); err != nil {
			t.Error(err)
		}
	})
	tenant, err := owned.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	now := time.Now()
	p := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, Capability: capability.Capability{
		Prefix: "owner", Digest: sha256.Sum256([]byte(raw)), Team: string(store.DefaultTeam), Audience: capability.Controller,
		Subject: capability.Subject{Kind: capability.SubjectLocal, ID: "account"}, Issuance: capability.Award,
		Unit: capability.UnitTrigger, UnitID: "unit", Purpose: capability.PurposeTrigger, Delivery: capability.DeliveryDirect,
		Grants: []capability.Grant{capability.TriggerHost}, IssuedAt: now.Add(-time.Minute),
		Binding: capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "account", Generation: 1},
	}}
	authenticated, storage := 0, 0
	options := client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: queueHostTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+raw {
			t.Fatal("local queue substituted peer identity for its award")
		}
		response := httptest.NewRecorder()
		switch r.URL.Path {
		case "/api/v1/runs/run/local-host/authentication":
			authenticated++
			snapshot := capability.AuthenticationSnapshot{Now: now, Authorities: []capability.AuthorityState{{Principal: p, MembershipValid: true}}, Binding: capability.BindingState{Current: p.Capability.Binding, LeaseExpiresAt: now.Add(time.Minute)}}
			if err := json.NewEncoder(response).Encode(localhostwire.LocalAuthentication{Snapshot: snapshot, Digests: [][32]byte{p.Capability.Digest}}); err != nil {
				t.Fatal(err)
			}
		case "/api/v1/runs/run/local-host/data-authority":
			storage++
			response.WriteHeader(http.StatusConflict)
			_, _ = response.WriteString(`{"error":{"code":"lock_held","message":"storage fence changed"}}`)
		default:
			t.Fatalf("unexpected queue service request: %s", r.URL)
		}
		return response.Result(), nil
	})}}
	binding := runners.StoreBinding{Tenant: tenant, Principal: p, Refresh: func(context.Context) (capability.Principal, error) { return p, nil }, Bearer: func(context.Context) (string, error) { return raw, nil }}
	services, err := newLocalNativeServices(t.Context(), t.TempDir(), owned, binding, options)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Message != "storage fence changed" || authenticated != 1 || storage != 1 || services.Close != nil {
		t.Fatalf("queue did not bind authenticated storage: authentication=%d storage=%d: %v", authenticated, storage, err)
	}
}
