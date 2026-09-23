package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestUnlicensedControllerHasNoMetering(t *testing.T) {
	f := newCreditsFixtureWithLicense(t, true, "") // A stored metered token must not bypass the license.
	for _, route := range []string{"/api/v1/credits", "/api/v1/credits/history", "/api/v1/credits/settings", "/api/v1/team/billing"} {
		status, _ := creditsRequest(t, http.MethodGet, f.url+route, f.admin, nil)
		if status != http.StatusNotFound && status != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 404 or 403", route, status)
		}
	}
	status, _ := creditsRequest(t, http.MethodPost, f.url+"/api/v1/tokens/"+f.prefix+"/metered", f.admin, map[string]any{"metered": true})
	if status != http.StatusNotFound && status != http.StatusForbidden {
		t.Errorf("mark token metered = %d, want 404 or 403", status)
	}
	status, _ = creditsRequest(t, http.MethodPost, f.url+"/api/v1/tokens", f.admin, map[string]any{
		"principal": "cloud", "kind": "runner", "scopes": []string{"nodes.claim"}, "metered": true,
	})
	if status != http.StatusNotFound && status != http.StatusForbidden {
		t.Errorf("mint metered token = %d, want 404 or 403", status)
	}
	status, body := creditsRequest(t, http.MethodGet, f.url+"/api/v1/capabilities", f.admin, nil)
	if status != http.StatusOK {
		t.Fatalf("capabilities = %d", status)
	}
	var caps struct {
		Billing *struct {
			Enabled bool `json:"enabled"`
		} `json:"billing"`
	}
	if err := json.Unmarshal(body, &caps); err != nil {
		t.Fatal(err)
	}
	if caps.Billing == nil || caps.Billing.Enabled {
		t.Fatal("unlicensed capabilities must advertise billing disabled")
	}
	ctx := context.Background()
	seedRunNode(t, f.store, "run-local", "build")
	if err := f.store.MarkNodeReady(ctx, "run-local", "build"); err != nil {
		t.Fatal(err)
	}
	n, err := client.NewWithToken(f.url, nil, f.runner).ClaimNode(ctx, "pod-1", nil, time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim with a legacy metered token = %+v, %v", n, err)
	}
	now := time.Now()
	if err := f.store.CreateTriggerWithRun(ctx, store.Trigger{
		ID: "trigger-local", Pipeline: "build", Status: "pending", CreatedAt: now,
	}, store.Run{ID: "trigger-local", Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	trigger, err := client.NewWithToken(f.url, nil, f.runner).ClaimTrigger(ctx)
	if err != nil || trigger == nil {
		t.Fatalf("trigger claim with a legacy metered token = %+v, %v", trigger, err)
	}
	charges, err := f.store.ListCreditCharges(ctx, 10)
	if err != nil || len(charges) != 0 {
		t.Fatalf("charges after claim = %+v, %v", charges, err)
	}
}

func TestLegacyMultiTeamLicenseAdvertisesBilling(t *testing.T) {
	f := newCreditsFixtureWithLicense(t, true, license.FeatureMultiTeam)
	status, body := creditsRequest(t, http.MethodGet, f.url+"/api/v1/capabilities", f.admin, nil)
	if status != http.StatusOK {
		t.Fatalf("capabilities = %d", status)
	}
	var caps struct {
		Billing struct {
			Enabled bool `json:"enabled"`
		} `json:"billing"`
	}
	if err := json.Unmarshal(body, &caps); err != nil {
		t.Fatal(err)
	}
	if !caps.Billing.Enabled {
		t.Fatal("legacy multi-team license did not advertise billing")
	}
	status, _ = creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits", f.admin, nil)
	if status != http.StatusOK {
		t.Fatalf("licensed credits route = %d, want 200", status)
	}
}
