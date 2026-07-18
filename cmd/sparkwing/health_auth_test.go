package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

func healthProfile(url string) *profile.Profile {
	return &profile.Profile{
		Name:       "test",
		Controller: &profile.ControllerSpec{URL: url},
	}
}

func healthServer(t *testing.T, authField string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","auth":"` + authField + `"}`))
	})
	return httptest.NewServer(mux)
}

func TestProbeController_WarnsWhenAuthDisabled(t *testing.T) {
	srv := healthServer(t, "disabled")
	defer srv.Close()

	r := probeController(context.Background(), healthProfile(srv.URL))
	if r.Status != "warn" {
		t.Fatalf("status=%q want warn", r.Status)
	}
	if !strings.Contains(r.Detail, "unauthenticated") {
		t.Fatalf("detail=%q want mention of unauthenticated", r.Detail)
	}
}

func TestProbeController_OKWhenAuthEnabled(t *testing.T) {
	srv := healthServer(t, "enabled")
	defer srv.Close()

	r := probeController(context.Background(), healthProfile(srv.URL))
	if r.Status != "ok" {
		t.Fatalf("status=%q want ok (detail=%q)", r.Status, r.Detail)
	}
}
