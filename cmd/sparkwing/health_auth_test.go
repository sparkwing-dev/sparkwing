package main

import (
	"net/http"
	"strings"
	"testing"
)

func healthProfile(url string) *namedProfile {
	return connectionFixture("health", url, "")
}

func mockControllerHealth(t *testing.T, authField string) {
	t.Helper()
	profileHTTPFixture(t, func(req *http.Request) (int, string) {
		if req.Method != "GET" || req.URL.Path != "/api/v1/health" || req.Header.Get("Authorization") != "" {
			t.Fatalf("health request: %s %s", req.Method, req.URL)
		}
		return 200, `{"status":"ok","auth":"` + authField + `"}`
	})
}

func TestProbeController_WarnsWhenAuthDisabled(t *testing.T) {
	mockControllerHealth(t, "disabled")

	r := probeController(t.Context(), healthProfile("https://controller.example"))
	if r.Status != "warn" {
		t.Fatalf("status=%q want warn", r.Status)
	}
	if !strings.Contains(r.Detail, "unauthenticated") {
		t.Fatalf("detail=%q want mention of unauthenticated", r.Detail)
	}
}

func TestProbeController_OKWhenAuthEnabled(t *testing.T) {
	mockControllerHealth(t, "enabled")

	r := probeController(t.Context(), healthProfile("https://controller.example"))
	if r.Status != "ok" {
		t.Fatalf("status=%q want ok (detail=%q)", r.Status, r.Detail)
	}
}
