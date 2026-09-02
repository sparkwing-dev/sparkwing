package localws

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOriginGuard_AllowsLocalCallersRejectsForeignSites(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		method      string
		host        string
		origin      string
		secFetch    string
		allowRemote bool
		want        int
	}{
		{name: "cli has no browser headers", method: http.MethodPost, host: "127.0.0.1:4343", want: http.StatusOK},
		{
			name: "dashboard same origin", method: http.MethodPost, host: "127.0.0.1:4343",
			origin: "http://127.0.0.1:4343", secFetch: "same-origin", want: http.StatusOK,
		},
		{
			name: "next dev on another loopback port", method: http.MethodPost, host: "localhost:4343",
			origin: "http://localhost:3100", secFetch: "same-site", want: http.StatusOK,
		},
		{
			name: "ipv6 loopback", method: http.MethodGet, host: "[::1]:4343",
			origin: "http://[::1]:4343", want: http.StatusOK,
		},
		{
			name: "foreign site posts", method: http.MethodPost, host: "127.0.0.1:4343",
			origin: "https://evil.example", secFetch: "cross-site", want: http.StatusForbidden,
		},
		{
			name: "foreign site reads", method: http.MethodGet, host: "127.0.0.1:4343",
			origin: "https://evil.example", secFetch: "cross-site", want: http.StatusForbidden,
		},
		{
			name: "opaque origin", method: http.MethodPost, host: "127.0.0.1:4343",
			origin: "null", secFetch: "cross-site", want: http.StatusForbidden,
		},
		{
			name: "cross-site post without origin", method: http.MethodPost, host: "127.0.0.1:4343",
			secFetch: "cross-site", want: http.StatusForbidden,
		},
		{
			name: "cross-site navigation", method: http.MethodGet, host: "127.0.0.1:4343",
			secFetch: "cross-site", want: http.StatusOK,
		},
		{name: "rebound host", method: http.MethodGet, host: "rebind.example:4343", want: http.StatusForbidden},
		{
			name: "loopback name suffix is not loopback", method: http.MethodGet,
			host: "127.0.0.1.rebind.example:4343", want: http.StatusForbidden,
		},
		{
			name: "remote host with opt-in", method: http.MethodPost, host: "10.0.0.9:4343",
			origin: "http://10.0.0.9:4343", allowRemote: true, want: http.StatusOK,
		},
		{
			name: "foreign site with opt-in", method: http.MethodPost, host: "10.0.0.9:4343",
			origin: "https://evil.example", allowRemote: true, want: http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			guard := originGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}), tc.allowRemote)

			req := httptest.NewRequest(tc.method, "/api/v1/triggers", strings.NewReader("{}"))
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.secFetch != "" {
				req.Header.Set("Sec-Fetch-Site", tc.secFetch)
			}
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestRun_RejectsCrossSiteAndReboundRequests(t *testing.T) {
	t.Parallel()

	addr := startLocalws(t, Options{Home: t.TempDir()})

	post := func(t *testing.T, host string, headers map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost,
			"http://"+addr+"/api/v1/triggers",
			strings.NewReader(`{"pipeline":"demo"}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if host != "" {
			req.Host = host
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	simpleCORS := map[string]string{
		"Origin":         "https://evil.example",
		"Sec-Fetch-Site": "cross-site",
		"Content-Type":   "text/plain;charset=UTF-8",
	}
	if got, _ := post(t, "", simpleCORS); got != http.StatusForbidden {
		t.Errorf("cross-origin no-cors POST status = %d, want 403", got)
	}
	if got, _ := post(t, "rebind.example", nil); got != http.StatusForbidden {
		t.Errorf("rebound host status = %d, want 403", got)
	}

	// the guard passes these through; the handler then rejects the
	// incomplete trigger body, which is what proves they reached it.
	reachedHandler := func(t *testing.T, label string, headers map[string]string) {
		t.Helper()
		status, body := post(t, "", headers)
		if status != http.StatusBadRequest || !strings.Contains(body, "trigger.source is required") {
			t.Errorf("%s status = %d body = %q, want the handler's 400", label, status, body)
		}
	}
	reachedHandler(t, "cli POST", map[string]string{"Content-Type": "application/json"})
	reachedHandler(t, "dashboard POST", map[string]string{
		"Origin":         "http://" + addr,
		"Sec-Fetch-Site": "same-origin",
		"Content-Type":   "application/json",
	})
}

func TestRun_RefusesNonLoopbackAddrWithoutOptIn(t *testing.T) {
	t.Parallel()

	err := Run(context.Background(), Options{Home: t.TempDir(), Addr: "0.0.0.0:4343"})
	if err == nil {
		t.Fatal("Run accepted a non-loopback addr without AllowRemote")
	}
	if !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("error = %v, want it to name the loopback requirement", err)
	}
}
