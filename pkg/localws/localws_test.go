package localws

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
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

func TestRun_LogStore_EndToEnd(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	logRoot := filepath.Join(t.TempDir(), "remote-logs")
	ls, err := fs.NewLogStore(logRoot)
	if err != nil {
		t.Fatalf("NewLogStore: %v", err)
	}
	if err := ls.Append(context.Background(), "run1", "node1",
		[]byte(`{"msg":"hello-from-fs"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	addr := startLocalws(t, Options{
		Home:          home,
		LogStore:      ls,
		LogStoreLabel: "fs",
	})

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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/v1/runs status = %d, want 405", resp.StatusCode)
	}
}

func TestRun_S3OnlyMode_ServesRuns(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	logRoot := filepath.Join(t.TempDir(), "remote-logs")
	ls, err := fs.NewLogStore(logRoot)
	if err != nil {
		t.Fatalf("NewLogStore: %v", err)
	}
	artRoot := filepath.Join(t.TempDir(), "remote-art")
	as, err := fs.NewArtifactStore(artRoot)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}

	dump := `{"kind":"run","data":{"id":"abc","pipeline":"p","status":"success","started_at":"2026-01-01T00:00:00Z"}}
{"kind":"node","data":{"run_id":"abc","node_id":"n1","status":"completed"}}
`
	if err := as.Put(context.Background(), "runs/abc/state.ndjson",
		strings.NewReader(dump)); err != nil {
		t.Fatalf("Put dump: %v", err)
	}

	addr := startLocalws(t, Options{
		Home:               home,
		LogStore:           ls,
		LogStoreLabel:      "fs",
		ArtifactStore:      as,
		ArtifactStoreLabel: "fs",
		NoLocalStore:       true,
	})

	resp := mustGet(t, "http://"+addr+"/api/v1/capabilities")
	defer resp.Body.Close()
	var caps backend.Capabilities
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		t.Fatalf("decode caps: %v", err)
	}
	if caps.Mode != "s3-only" {
		t.Errorf("caps.Mode = %q, want s3-only", caps.Mode)
	}
	if caps.Storage.Runs != "s3" {
		t.Errorf("caps.Storage.Runs = %q, want s3", caps.Storage.Runs)
	}
	if !caps.ReadOnly {
		t.Errorf("caps.ReadOnly = false, want true")
	}

	resp2 := mustGet(t, "http://"+addr+"/api/v1/runs")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("/runs status = %d", resp2.StatusCode)
	}
	var listed struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&listed); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(listed.Runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(listed.Runs))
	}
	if listed.Runs[0]["id"] != "abc" {
		t.Errorf("runs[0].id = %v", listed.Runs[0]["id"])
	}

	resp3 := mustGet(t, "http://"+addr+"/api/v1/runs/abc?include=nodes")
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("/runs/abc status = %d", resp3.StatusCode)
	}
	var wrap struct {
		Run   map[string]any   `json:"run"`
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&wrap); err != nil {
		t.Fatalf("decode get-run: %v", err)
	}
	if wrap.Run["id"] != "abc" {
		t.Errorf("wrap.run.id = %v", wrap.Run["id"])
	}
	if len(wrap.Nodes) != 1 {
		t.Errorf("got %d nodes, want 1", len(wrap.Nodes))
	}

	cancelReq, _ := http.NewRequest(http.MethodPost,
		"http://"+addr+"/api/v1/runs/abc/cancel", nil)
	resp4, err := http.DefaultClient.Do(cancelReq)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode == http.StatusOK || resp4.StatusCode == http.StatusNoContent {
		t.Errorf("cancel status = %d, want non-2xx (no controller in s3-only mode)", resp4.StatusCode)
	}
}

func TestRun_ArtifactsEndpoint(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	artRoot := filepath.Join(t.TempDir(), "remote-art")
	as, err := fs.NewArtifactStore(artRoot)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	if err := as.Put(context.Background(), "abcd1234",
		readerOf("hello-artifact")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	addr := startLocalws(t, Options{
		Home:               home,
		ArtifactStore:      as,
		ArtifactStoreLabel: "fs",
	})

	resp := mustGet(t, "http://"+addr+"/api/v1/artifacts/abcd1234")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello-artifact" {
		t.Errorf("body = %q", body)
	}
}

func startLocalws(t *testing.T, opts Options) string {
	t.Helper()
	if reason := web.BundleSkipReason(); reason != "" {
		t.Skip(reason)
	}
	ln := pickListener(t)
	t.Cleanup(func() { _ = ln.Close() })
	opts.Listener = ln
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = Run(ctx, opts)
		close(done)
	}()
	var stopOnce sync.Once
	var stopErr error
	stopReported := false
	runErrReported := false
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				stopErr = fmt.Errorf("localws did not stop within 1s")
			}
		})
		if stopErr != nil && !stopReported {
			stopReported = true
			t.Errorf("stop localws: %v", stopErr)
		}
		if stopErr == nil && runErr != nil && !runErrReported {
			runErrReported = true
			t.Errorf("localws exited: %v", runErr)
		}
	}
	t.Cleanup(stop)

	client := &http.Client{Timeout: 250 * time.Millisecond}
	retry := time.NewTicker(20 * time.Millisecond)
	defer retry.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-done:
			runErrReported = true
			t.Fatalf("localws exited before readiness: %v", runErr)
		default:
		}
		resp, err := client.Get("http://" + addr + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			return addr
		}
		select {
		case <-done:
			runErrReported = true
			t.Fatalf("localws exited before readiness: %v", runErr)
		case <-retry.C:
		case <-deadline.C:
			stop()
			t.Fatal("localws did not start in time")
		}
	}
}

func pickListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
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

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<html><head><script src="/sparkwing-runtime.js"></script></head><body></body></html>`)},
	}
	handler := buildHandler(ctx, cancel, Options{Addr: "127.0.0.1:4343", Version: "v1.2.3"}, handlerParts{
		paths:   paths,
		backend: backend.NewStoreBackend(st, paths, nil),
		store:   st,
	}, bundle)
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

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html><body>stub dashboard</body></html>")},
	}
	handler := buildHandler(ctx, cancel, Options{Addr: "127.0.0.1:4343", Version: "v1.2.3"}, handlerParts{
		paths:   paths,
		backend: backend.NewStoreBackend(st, paths, nil),
		store:   st,
		ctrl:    controller.New(st, nil),
	}, bundle)
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
