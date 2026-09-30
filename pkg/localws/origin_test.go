package localws

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

func TestLoopbackBind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		addr string
		want bool
	}{
		{addr: "127.0.0.1:4343", want: true},
		{addr: "localhost:4343", want: true},
		{addr: "localhost.:4343", want: true},
		{addr: "[::1]:4343", want: true},
		{addr: "127.0.0.2:4343", want: true},
		{addr: "127.0.0.1", want: true},
		{addr: "0.0.0.0:4343", want: false},
		{addr: ":4343", want: false},
		{addr: "", want: false},
		{addr: "192.168.1.20:4343", want: false},
		{addr: "127.0.0.1.rebind.example:4343", want: false},
	}
	for _, tc := range cases {
		if got := LoopbackBind(tc.addr); got != tc.want {
			t.Errorf("LoopbackBind(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestBuildHandler_GuardsTheServedChain(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths := orchestrator.PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html><body>stub dashboard</body></html>")},
	}
	handler := buildHandler(ctx, cancel, Options{Addr: "127.0.0.1:4343"}, handlerParts{
		paths:   paths,
		backend: backend.NewStoreBackend(st, paths, nil),
		store:   st,
		ctrl:    controller.New(st, nil),
	}, bundle)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	do := func(t *testing.T, method, path, host string, headers map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"pipeline":"demo"}`))
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
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if got, _ := do(t, http.MethodPost, "/api/v1/triggers", "", map[string]string{
		"Origin":         "https://evil.example",
		"Sec-Fetch-Site": "cross-site",
		"Content-Type":   "text/plain;charset=UTF-8",
	}); got != http.StatusForbidden {
		t.Errorf("cross-origin no-cors POST status = %d, want 403", got)
	}
	if got, _ := do(t, http.MethodPost, "/api/v1/triggers", "rebind.example", nil); got != http.StatusForbidden {
		t.Errorf("rebound host status = %d, want 403", got)
	}
	if got, _ := do(t, http.MethodGet, "/", "", map[string]string{
		"Sec-Fetch-Site": "cross-site",
		"Sec-Fetch-Dest": "image",
	}); got != http.StatusForbidden {
		t.Errorf("cross-site subresource status = %d, want 403", got)
	}

	status, body := do(t, http.MethodGet, "/", "", nil)
	if status != http.StatusOK || !strings.Contains(body, "stub dashboard") {
		t.Errorf("local dashboard GET = %d body = %q, want the served bundle", status, body)
	}
	status, body = do(t, http.MethodPost, "/api/v1/triggers", "", map[string]string{
		"Content-Type": "application/json",
	})
	if status != http.StatusBadRequest || !strings.Contains(body, "trigger.source is required") {
		t.Errorf("local POST status = %d body = %q, want the handler's 400", status, body)
	}
}
