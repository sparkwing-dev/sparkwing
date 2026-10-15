package web_test

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type logPageFixture struct {
	backend.Backend
	runID, jobID string
	body         []byte
}

func seedRun(t *testing.T, runID, jobID, message string) *logPageFixture {
	t.Helper()
	raw, err := json.Marshal(sparkwing.LogRecord{TS: time.Now(), Level: "info", JobID: jobID, Msg: message})
	if err != nil {
		t.Fatal(err)
	}
	return &logPageFixture{runID: runID, jobID: jobID, body: append(raw, '\n')}
}

func (f *logPageFixture) GetRun(context.Context, string) (*store.Run, error) {
	return &store.Run{ID: f.runID, Pipeline: "ci", Status: "success"}, nil
}
func (f *logPageFixture) ListNodes(context.Context, string) ([]*store.Node, error) {
	return []*store.Node{{RunID: f.runID, NodeID: f.jobID, Status: "done", Outcome: "success"}}, nil
}
func (*logPageFixture) ListEventsAfter(context.Context, string, int64, int) ([]store.Event, error) {
	return nil, nil
}
func (f *logPageFixture) ReadNodeLog(context.Context, string, string, backend.ReadOpts) ([]byte, error) {
	return f.body, nil
}
func (*logPageFixture) StreamNodeLog(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
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

func startServer(b backend.Backend) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/runs/{id}/logs", web.RunLogsHandler(b))
	mux.Handle("GET /api/v1/runs/{id}/logs/{node}", web.NodeLogsHandler(b))
	mux.Handle("/", web.Pages(fixtureShell()))
	return web.SecurityHeaders(false, mux)
}

func TestAPI_Logs(t *testing.T) {
	handler := startServer(seedRun(t, "run-web-ok", "web-ok", "web hello"))

	logs := mustGetText(t, handler, "/api/v1/runs/run-web-ok/logs/web-ok")
	if !strings.Contains(logs, "web hello") {
		t.Fatalf("node logs missing 'web hello': %q", logs)
	}

	all := mustGetText(t, handler, "/api/v1/runs/run-web-ok/logs")
	if !strings.Contains(all, "=== web-ok (success) ===") {
		t.Fatalf("whole-run logs missing banner: %q", all)
	}
	if !strings.Contains(all, "web hello") {
		t.Fatalf("whole-run logs missing content: %q", all)
	}
}

func TestAPI_StaticIndexServed(t *testing.T) {
	handler := startServer(&logPageFixture{})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	resp := recorder.Result()
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
	handler := startServer(seedRun(t, "run-web-ansi", "web-ansi", "\x1b[31mansi-hello\x1b[0m"))

	url := "/api/v1/runs/run-web-ansi/logs/web-ansi"

	defaultBody := mustGetText(t, handler, url)
	if strings.Contains(defaultBody, `"msg":`) || strings.Contains(defaultBody, `"event":`) {
		t.Fatalf("default response still looks like JSONL:\n%s", defaultBody)
	}
	if !strings.Contains(defaultBody, "ansi-hello") {
		t.Fatalf("default response missing log content:\n%s", defaultBody)
	}
	if strings.ContainsRune(defaultBody, 0x1b) {
		t.Fatalf("default response leaked ANSI escapes:\n%q", defaultBody)
	}

	ansiBody := mustGetTextWithAccept(t, handler, url, "text/x-ansi")
	if !strings.ContainsRune(ansiBody, 0x1b) {
		t.Fatalf("text/x-ansi response had no escapes:\n%q", ansiBody)
	}
	if !strings.Contains(ansiBody, "ansi-hello") {
		t.Fatalf("text/x-ansi response missing log content:\n%s", ansiBody)
	}

	rawBody := mustGetTextWithAccept(t, handler, url, "application/x-ndjson")
	if !strings.Contains(rawBody, `"msg":`) {
		t.Fatalf("raw response missing JSONL envelope:\n%s", rawBody)
	}
}

func mustGetTextWithAccept(t *testing.T, handler http.Handler, url, accept string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", accept)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	resp := recorder.Result()
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

func mustGetText(t *testing.T, handler http.Handler, url string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
	resp := recorder.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
