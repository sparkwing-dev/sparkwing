package web_test

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func seedRun(t *testing.T, paths orchestrator.Paths, runID, nodeID, msg string) {
	t.Helper()
	ctx := context.Background()
	if err := paths.EnsureRunDir(runID); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: nodeID, Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.StartNode(ctx, runID, nodeID); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(ctx, runID, nodeID, "success", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, runID, "success", ""); err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(sparkwing.LogRecord{TS: time.Now(), Level: "info", JobID: nodeID, Msg: msg})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.NodeLog(runID, nodeID), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// safety: the real bundle is gitignored, so a checkout that has not built the
// dashboard has none to serve; the suite carries its own shell instead.
func fixtureShell() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<!doctype html><title>Sparkwing</title>` +
				`<script src="/sparkwing-runtime.js"></script><div id="app">dashboard shell</div>`)},
	}
}

func startServer(t *testing.T, paths orchestrator.Paths) (string, func()) {
	t.Helper()
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	b := backend.NewStoreBackend(st, paths, nil)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/runs/{id}/logs", web.RunLogsHandler(b))
	mux.Handle("GET /api/v1/runs/{id}/logs/{node}", web.NodeLogsHandler(b))
	mux.Handle("/", web.Pages(fixtureShell()))
	srv := httptest.NewServer(web.SecurityHeaders(false, mux))
	stop := func() {
		srv.Close()
		_ = st.Close()
	}
	t.Cleanup(stop)
	return srv.URL, stop
}

func TestAPI_Logs(t *testing.T) {
	root := t.TempDir()
	paths := orchestrator.PathsAt(root)

	seedRun(t, paths, "run-web-ok", "web-ok", "web hello")

	base, stop := startServer(t, paths)
	defer stop()

	logs := mustGetText(t, base+"/api/v1/runs/run-web-ok/logs/web-ok")
	if !strings.Contains(logs, "web hello") {
		t.Fatalf("node logs missing 'web hello': %q", logs)
	}

	all := mustGetText(t, base+"/api/v1/runs/run-web-ok/logs")
	if !strings.Contains(all, "=== web-ok (success) ===") {
		t.Fatalf("whole-run logs missing banner: %q", all)
	}
	if !strings.Contains(all, "web hello") {
		t.Fatalf("whole-run logs missing content: %q", all)
	}
}

func TestAPI_StaticIndexServed(t *testing.T) {
	root := t.TempDir()
	paths := orchestrator.PathsAt(root)
	base, stop := startServer(t, paths)
	defer stop()

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<title>Sparkwing</title>") {
		t.Fatalf("index missing expected title: %s", string(body))
	}
	if strings.Contains(string(body), "__SPARKWING_TOKEN__") {
		t.Fatalf("index carries a runtime bearer slot: %s", string(body))
	}
}

func TestAPI_LogsAcceptNegotiation(t *testing.T) {
	root := t.TempDir()
	paths := orchestrator.PathsAt(root)

	seedRun(t, paths, "run-web-ansi", "web-ansi", "\x1b[31mansi-hello\x1b[0m")

	base, stop := startServer(t, paths)
	defer stop()

	url := base + "/api/v1/runs/run-web-ansi/logs/web-ansi"

	defaultBody := mustGetText(t, url)
	if strings.Contains(defaultBody, `"msg":`) || strings.Contains(defaultBody, `"event":`) {
		t.Fatalf("default response still looks like JSONL:\n%s", defaultBody)
	}
	if !strings.Contains(defaultBody, "ansi-hello") {
		t.Fatalf("default response missing log content:\n%s", defaultBody)
	}
	if strings.ContainsRune(defaultBody, 0x1b) {
		t.Fatalf("default response leaked ANSI escapes:\n%q", defaultBody)
	}

	ansiBody := mustGetTextWithAccept(t, url, "text/x-ansi")
	if !strings.ContainsRune(ansiBody, 0x1b) {
		t.Fatalf("text/x-ansi response had no escapes:\n%q", ansiBody)
	}
	if !strings.Contains(ansiBody, "ansi-hello") {
		t.Fatalf("text/x-ansi response missing log content:\n%s", ansiBody)
	}

	rawBody := mustGetTextWithAccept(t, url, "application/x-ndjson")
	if !strings.Contains(rawBody, `"msg":`) {
		t.Fatalf("raw response missing JSONL envelope:\n%s", rawBody)
	}
}

func mustGetTextWithAccept(t *testing.T, url, accept string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", accept)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s (Accept=%s): %v", url, accept, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s (Accept=%s): status %d", url, accept, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, accept) {
		t.Fatalf("Content-Type = %q, want prefix %q", ct, accept)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func mustGetText(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
