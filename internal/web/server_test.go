package web_test

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var registerOnce sync.Map

func register(name string, factory func() sparkwing.Pipeline[sparkwing.NoInputs]) {
	if _, loaded := registerOnce.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	sparkwing.Register[sparkwing.NoInputs](name, factory)
}

type webOK struct{ sparkwing.Base }

func (webOK) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error {
		sparkwing.Info(ctx, "web hello")
		return nil
	})
	return nil
}

type webDAG struct{ sparkwing.Base }

func (webDAG) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	a := sparkwing.Job(plan, "a", func(ctx context.Context) error { return nil })
	sparkwing.Job(plan, "b", func(ctx context.Context) error { return nil }).Needs(a)
	return nil
}

type webANSI struct{ sparkwing.Base }

func (webANSI) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "web-ansi", func(ctx context.Context) error {
		sparkwing.Info(ctx, "\x1b[31mansi-hello\x1b[0m")
		return nil
	})
	return nil
}

func init() {
	register("web-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &webOK{} })
	register("web-dag", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &webDAG{} })
	register("web-ansi", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &webANSI{} })
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

	res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "web-ok"})
	if err != nil {
		t.Fatalf("orchestrator.Run: %v", err)
	}

	base, stop := startServer(t, paths)
	defer stop()

	logs := mustGetText(t, base+"/api/v1/runs/"+res.RunID+"/logs/web-ok")
	if !strings.Contains(logs, "web hello") {
		t.Fatalf("node logs missing 'web hello': %q", logs)
	}

	all := mustGetText(t, base+"/api/v1/runs/"+res.RunID+"/logs")
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

	res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{Pipeline: "web-ansi"})
	if err != nil {
		t.Fatalf("orchestrator.Run: %v", err)
	}

	base, stop := startServer(t, paths)
	defer stop()

	url := base + "/api/v1/runs/" + res.RunID + "/logs/web-ansi"

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
