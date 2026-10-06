package web_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/web"
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

// A token-backed dashboard without sign-in on a non-loopback address answers
// any Host, so it cannot refuse rebound names, but a page on another site
// must not drive the service token through a visitor's browser. Same-origin
// browser writes and clients that send neither Origin nor Sec-Fetch-Site,
// such as curl, still reach the controller.
func TestUnauthenticatedRemoteDashboardRefusesCrossSiteWrites(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(controller.Close)
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done)
		serveErr = web.ServeWithOptions(ctx, web.HandlerOptions{
			Bundle: fixtureShell(), Listener: ln, Paths: orchestrator.PathsAt(t.TempDir()),
			ControllerURL: controller.URL, Token: "svc-token", AllowUnauthenticatedRemote: true,
		}, ln.Addr().String())
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	host := fmt.Sprintf("dash.internal:%d", port)
	reqCtx, stopRequests := context.WithCancel(t.Context())
	defer stopRequests()
	go func() {
		<-done
		stopRequests()
	}()

	for _, tc := range []struct {
		name, method, origin, site string
		refused                    bool
		reason                     string
	}{
		{"cross-site form post", http.MethodPost, "https://attacker.example", "cross-site", true, "Origin https://attacker.example"},
		{"opaque origin", http.MethodPost, "null", "cross-site", true, "Origin null"},
		{"same-site sibling without origin", http.MethodPost, "", "same-site", true, "Sec-Fetch-Site is same-site"},
		{"cross-site delete", http.MethodDelete, "https://attacker.example", "", true, "Origin https://attacker.example"},
		{"same-origin dashboard", http.MethodPost, "http://" + host, "same-origin", false, ""},
		{"curl", http.MethodPost, "", "", false, ""},
		{"cross-site read", http.MethodGet, "https://attacker.example", "cross-site", false, ""},
	} {
		path := "/api/v1/runs/run-1/cancel"
		if tc.method == http.MethodGet {
			path = "/api/v1/runs"
		}
		req, err := http.NewRequestWithContext(reqCtx, tc.method, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.site != "" {
			req.Header.Set("Sec-Fetch-Site", tc.site)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			select {
			case <-done:
				t.Fatalf("%s: web server exited: %v", tc.name, serveErr)
			default:
				t.Fatalf("%s: %v", tc.name, err)
			}
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		mu.Lock()
		reached := len(seen) > 0
		seen = nil
		mu.Unlock()
		if tc.refused {
			if resp.StatusCode != http.StatusForbidden || reached || !strings.Contains(string(body), tc.reason) {
				t.Errorf("%s: status %d, reached controller %v, body %q; want 403 naming %q before the controller",
					tc.name, resp.StatusCode, reached, body, tc.reason)
			}
			continue
		}
		if resp.StatusCode == http.StatusForbidden || !reached {
			t.Errorf("%s: status %d, reached controller %v; want the request proxied", tc.name, resp.StatusCode, reached)
		}
	}
}
