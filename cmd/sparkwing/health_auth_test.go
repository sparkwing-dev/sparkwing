package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/discovery"
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

func logsProbeProfile(t *testing.T, logsAuth string) *profile.Profile {
	t.Helper()
	logs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","auth":"` + logsAuth + `"}`))
	}))
	t.Cleanup(logs.Close)

	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"logs":"` + logs.URL + `"}`))
	}))
	t.Cleanup(controller.Close)

	discovery.ResetCache()
	t.Cleanup(discovery.ResetCache)
	return healthProfile(controller.URL)
}

func TestProbeLogs_WarnsWhenTheLogsServiceServesUnauthenticated(t *testing.T) {
	r := probeLogs(context.Background(), logsProbeProfile(t, "disabled"))
	if r.Status != "warn" {
		t.Fatalf("status=%q want warn (detail=%q)", r.Status, r.Detail)
	}
	if !strings.Contains(r.Detail, "unauthenticated") {
		t.Fatalf("detail=%q want mention of unauthenticated", r.Detail)
	}
}

func TestProbeLogs_OKWhenTheLogsServiceResolvesTokens(t *testing.T) {
	r := probeLogs(context.Background(), logsProbeProfile(t, "enabled"))
	if r.Status != "ok" {
		t.Fatalf("status=%q want ok (detail=%q)", r.Status, r.Detail)
	}
}
