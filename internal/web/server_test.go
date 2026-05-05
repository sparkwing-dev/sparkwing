package web_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Registration is guarded to avoid duplicate panics across test
// invocations.
var registerOnce sync.Map

func register(name string, factory func() sparkwing.Pipeline[sparkwing.NoInputs]) {
	if _, loaded := registerOnce.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	sparkwing.Register[sparkwing.NoInputs](name, factory)
}

type webOK struct{ sparkwing.Base }

func (webOK) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, sparkwing.JobFn(func(ctx context.Context) error {
		sparkwing.Info(ctx, "web hello")
		return nil
	}))
	return nil
}

type webDAG struct{ sparkwing.Base }

func (webDAG) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	a := sparkwing.Job(plan, "a", sparkwing.JobFn(func(ctx context.Context) error { return nil }))
	sparkwing.Job(plan, "b", sparkwing.JobFn(func(ctx context.Context) error { return nil })).Needs(a)
	return nil
}

// webANSI emits a Msg with a real SGR escape so negotiation tests can
// verify plain strips it and text/x-ansi preserves it.
type webANSI struct{ sparkwing.Base }

func (webANSI) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "web-ansi", sparkwing.JobFn(func(ctx context.Context) error {
		sparkwing.Info(ctx, "\x1b[31mansi-hello\x1b[0m")
		return nil
	}))
	return nil
}

func init() {
	register("web-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &webOK{} })
	register("web-dag", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &webDAG{} })
	register("web-ansi", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &webANSI{} })
}

// startServer spins up the web server on an ephemeral port.
func startServer(t *testing.T, paths orchestrator.Paths) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = web.Serve(ctx, paths, addr)
		close(done)
	}()

	base := fmt.Sprintf("http://%s", addr)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(base + "/api/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return base, func() {
					cancel()
					<-done
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("web server did not become ready")
	return "", func() {}
}

// TestAPI_Logs covers the dashboard-owned log endpoints under
// /api/v1/runs/{id}/logs[/{node}].
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
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<title>Sparkwing</title>") {
		t.Fatalf("index missing expected title: %s", string(body))
	}
	// Go-side templating must leave the runtime markers consumable:
	// the React shim in api.ts treats both the literal marker and an
	// empty string as "not configured".
	if !strings.Contains(string(body), `window.__SPARKWING_TOKEN__="";`) {
		t.Fatalf("token template not substituted in index")
	}
}

// TestAPI_LogsAcceptNegotiation pins the format negotiation contract:
//   - Default: pretty plain text, no ANSI anywhere.
//   - Accept: text/x-ansi: pretty + renderer SGR + Msg ANSI passthrough.
//   - Accept: application/x-ndjson: raw JSONL envelope intact.
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
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", accept)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s (Accept=%s): %v", url, accept, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
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
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
