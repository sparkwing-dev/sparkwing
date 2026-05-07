package localws

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

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

	// Capabilities reports the configured backend.
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

	// Dashboard log read goes through the LogStore.
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

	// GETs still work.
	resp := mustGet(t, "http://"+addr+"/api/v1/health")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d", resp.StatusCode)
	}

	// POST to a state-mutating endpoint -> 405.
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

	// Capabilities reports s3-only / runs:s3.
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

	// Runs list comes from the bucket.
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

	// Single-run detail with ?include=nodes.
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

	// Mutating routes have no controller behind them in s3-only mode.
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

// startLocalws starts Run on a pre-built ephemeral-port listener and
// returns the bound address once the server is live. Handing the
// listener to Run avoids the close-then-rebind window that flakes
// under -parallel: the port is held continuously from pick to serve.
func startLocalws(t *testing.T, opts Options) string {
	t.Helper()
	ln := pickListener(t)
	opts.Listener = ln
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() { done <- Run(ctx, opts) }()

	// Wait for the server to come up.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			return addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("localws did not start in time")
	return addr
}

// pickListener reserves a 127.0.0.1 ephemeral port and returns the
// open listener. Caller hands it to Run via Options.Listener; Run
// takes ownership and closes it on shutdown.
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
