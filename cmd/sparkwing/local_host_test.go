package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type queueHostTransport func(*http.Request) (*http.Response, error)

func (f queueHostTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
