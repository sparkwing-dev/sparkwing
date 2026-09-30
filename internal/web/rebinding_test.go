package web_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

// A dashboard without sign-in on loopback answers only its own loopback
// names: a page whose name was rebound to 127.0.0.1 keeps that name in
// Host, and a write from another site carries that site's Origin.
func TestServeWithoutLoginRefusesReboundAndCrossSiteRequests(t *testing.T) {
	base, _ := startServer(t, orchestrator.PathsAt(t.TempDir()))
	port := base[strings.LastIndex(base, ":")+1:]

	send := func(method, path, host, origin string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, base+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
			req.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	for _, tc := range []struct {
		name, method, path, host, origin string
		refused                          bool
	}{
		{"loopback page", http.MethodGet, "/", "127.0.0.1:" + port, "", false},
		{"loopback api read", http.MethodGet, "/api/v1/runs", "localhost:" + port, "", false},
		{"rebound page", http.MethodGet, "/", "rebind.example:" + port, "", true},
		{"rebound api read", http.MethodGet, "/api/v1/runs", "rebind.example:" + port, "", true},
		{"cross-site write", http.MethodPost, "/api/v1/triggers", "127.0.0.1:" + port, "https://evil.example", true},
	} {
		code := send(tc.method, tc.path, tc.host, tc.origin)
		if got := code == http.StatusForbidden; got != tc.refused {
			t.Errorf("%s: %s %s Host %s = %d, refused %v want %v", tc.name, tc.method, tc.path, tc.host, code, got, tc.refused)
		}
	}
}
