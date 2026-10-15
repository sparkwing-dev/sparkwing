package localws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestLocalPaths_ExplicitHomeDoesNotMutateEnvironment(t *testing.T) {
	original := filepath.Join(t.TempDir(), "original-home")
	t.Setenv("SPARKWING_HOME", original)
	explicit := filepath.Join(t.TempDir(), "explicit-home")

	paths, err := localPaths(explicit)
	if err != nil {
		t.Fatalf("localPaths: %v", err)
	}
	if paths.Root != explicit {
		t.Fatalf("paths.Root = %q, want explicit home %q", paths.Root, explicit)
	}
	if got := os.Getenv("SPARKWING_HOME"); got != original {
		t.Fatalf("SPARKWING_HOME = %q, want unchanged %q", got, original)
	}
}

func TestRun_ReadsRunDirectoryLogs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}

	home := t.TempDir()
	paths := orchestrator.PathsAt(home)
	if err := paths.EnsureRunDir("run1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.NodeLog("run1", "node1"), []byte(`{"msg":"hello-from-fs"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	seed, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	// safety: a log read is answered only for a run this store records, because the team boundary checks the run.
	if err := seed.CreateRun(context.Background(), store.Run{ID: "run1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	addr := startLocalws(t, Options{Home: home})

	resp := mustGet(t, "http://"+addr+"/api/v1/capabilities")
	defer resp.Body.Close()
	var caps backend.Capabilities
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if caps.Storage.Logs != "fs" {
		t.Errorf("storage.logs = %q, want fs", caps.Storage.Logs)
	}
	if caps.Storage.Runs != "sqlite" {
		t.Errorf("storage.runs = %q, want sqlite", caps.Storage.Runs)
	}

	resp = mustGet(t, "http://"+addr+"/api/v1/runs/run1/logs/node1")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello-from-fs") {
		t.Errorf("log body = %q, want hello-from-fs", body)
	}
}

func TestRun_ReadOnly_BlocksWrites(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	home := t.TempDir()
	addr := startLocalws(t, Options{
		Home:     home,
		ReadOnly: true,
	})

	resp := mustGet(t, "http://"+addr+"/api/v1/health")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost,
		"http://"+addr+"/api/v1/runs", strings.NewReader("{}"))
	resp, err := authedDo(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/v1/runs status = %d, want 405", resp.StatusCode)
	}
}

func fixtureShell() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<!doctype html><title>Sparkwing</title>` +
				`<script src="/sparkwing-runtime.js"></script><div id="app">dashboard shell</div>`)},
	}
}

func startLocalws(t *testing.T, opts Options) string {
	t.Helper()
	paths, err := localPaths(opts.Home)
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	token, err := paths.EnsureServeToken()
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	ring, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Bundle == nil {
		opts.Bundle = fixtureShell()
	}
	ctrl := controller.New(state, nil).WithSecretsCipher(ring.For(state)).WithLocalExecution().WithDashboard(controller.Dashboard{
		Local: true, Bundle: opts.Bundle, Version: opts.Version, Paths: paths,
		Capabilities: backend.Capabilities{Mode: "local", Storage: backend.CapabilitiesStorage{Artifacts: "fs", Logs: "fs", Runs: "sqlite"}, Features: localFeatures()},
	})
	srv := httptest.NewUnstartedServer(nil)
	opts.Addr = srv.Listener.Addr().String()
	srv.Config.Handler = buildHandler(opts, handlerParts{paths: paths, store: state, ctrl: ctrl, serveToken: token})
	srv.Start()
	t.Cleanup(srv.Close)
	serveTokens.Store(opts.Addr, token)
	t.Cleanup(func() { serveTokens.Delete(opts.Addr) })
	return opts.Addr
}

var serveTokens sync.Map

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := authedDo(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func authedPost(url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return authedDo(req)
}

func authedDo(req *http.Request) (*http.Response, error) {
	if token, ok := serveTokens.Load(req.URL.Host); ok {
		req.Header.Set("Authorization", "Bearer "+token.(string))
	}
	return http.DefaultClient.Do(req)
}

func readerOf(s string) io.Reader { return strings.NewReader(s) }

func TestBuildHandler_ServesDashboardConfigAndSecurityHeaders(t *testing.T) {
	paths, err := localPaths(t.TempDir())
	if err != nil {
		t.Fatalf("localPaths: %v", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<html><head><script src="/sparkwing-runtime.js"></script></head><body></body></html>`)},
	}
	handler := buildHandler(Options{Addr: "127.0.0.1:4343", Version: "v1.2.3"}, handlerParts{
		paths: paths,
		store: st,
		ctrl:  controller.New(st, nil).WithDashboard(controller.Dashboard{Local: true, Bundle: bundle, Version: "v1.2.3"}),
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/sparkwing-runtime.js")
	if err != nil {
		t.Fatalf("get runtime config: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("runtime config = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), `window.__SPARKWING_VERSION__="v1.2.3"`) {
		t.Errorf("runtime config lost the CLI version: %s", body)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none'", got)
	}
}

func TestBuildHandler_SecurityHeadersOnEveryLocalRoute(t *testing.T) {
	paths, err := localPaths(t.TempDir())
	if err != nil {
		t.Fatalf("localPaths: %v", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html><body>stub dashboard</body></html>")},
	}
	handler := buildHandler(Options{Addr: "127.0.0.1:4343", Version: "v1.2.3"}, handlerParts{
		paths: paths,
		store: st,
		ctrl:  controller.New(st, nil).WithDashboard(controller.Dashboard{Local: true, Bundle: bundle, Version: "v1.2.3"}),
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	for _, path := range []string{
		"/",
		"/sparkwing-runtime.js",
		"/api/v1/version",
		"/api/v1/queue",
		"/api/v1/pipelines",
		"/api/v1/capabilities",
		"/api/v1/runs",
		"/api/v1/crons",
	} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("get %s: %v", path, err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q, want DENY", got)
			}
			if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
				t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none'", got)
			}
			if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
				t.Errorf("Strict-Transport-Security = %q, want none on a plain HTTP dashboard", got)
			}
			if strings.HasPrefix(path, "/api/") && resp.StatusCode == http.StatusOK {
				if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
					t.Errorf("%s Content-Type = %q, want JSON: %s", path, ct, body)
				}
			}
		})
	}
}
