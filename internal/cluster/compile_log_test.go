package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/bincache"
)

// TestShipCompileOutput_PostsToLogsServer verifies IMP-001's wiring:
// when the warm-runner's .sparkwing/ compile fails, the captured
// `go build` stdout + stderr are POSTed to the synthetic
// CompileLogNode log on the configured logs service.
func TestShipCompileOutput_PostsToLogsServer(t *testing.T) {
	const runID = "run-test-imp-001"
	const want = "go: go.mod requires go >= 9.99.0\n./pipeline.go:7:2: undefined: Foo"

	var (
		mu      sync.Mutex
		gotPath string
		gotBody []byte
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath = r.URL.Path
		gotBody = body
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	buildErr := &bincache.CompileError{Output: []byte(want), Err: errors.New("exit status 1")}
	opts := TriggerLoopOptions{LogsURL: ts.URL, Token: "ignored"}
	shipCompileOutput(context.Background(), opts, runID, buildErr, slog.Default())

	mu.Lock()
	defer mu.Unlock()
	wantPath := "/api/v1/logs/" + runID + "/" + CompileLogNode
	if gotPath != wantPath {
		t.Errorf("path: got %q, want %q", gotPath, wantPath)
	}
	if string(gotBody) != want {
		t.Errorf("body: got %q, want %q", gotBody, want)
	}
}

// TestShipCompileOutput_NoLogsURLNoOp guards the laptop-local path
// where opts.LogsURL is empty: shipping must be a silent no-op
// rather than panicking on a nil client.
func TestShipCompileOutput_NoLogsURLNoOp(t *testing.T) {
	buildErr := &bincache.CompileError{Output: []byte("oops"), Err: errors.New("x")}
	shipCompileOutput(context.Background(), TriggerLoopOptions{}, "run-x", buildErr, slog.Default())
}

// TestShipCompileOutput_NonCompileErrorIgnored ensures we don't post
// anything when the build error isn't a *CompileError (e.g. a fetch
// or hash failure earlier in triggerBuildOrFetchBinary).
func TestShipCompileOutput_NonCompileErrorIgnored(t *testing.T) {
	posted := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	opts := TriggerLoopOptions{LogsURL: ts.URL}
	shipCompileOutput(context.Background(), opts, "run-y", errors.New("hash failed"), slog.Default())
	if posted {
		t.Errorf("expected no POST for non-CompileError; server saw a request")
	}
}

// TestShipCompileOutput_PostsEvenWhenCtxCancelled exercises the
// context.WithoutCancel guard: the heartbeat goroutine often
// cancels the parent ctx by the time we ship, but we still want
// the operator-facing diagnostic to land.
func TestShipCompileOutput_PostsEvenWhenCtxCancelled(t *testing.T) {
	var posted sync.WaitGroup
	posted.Add(1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		posted.Done()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	buildErr := &bincache.CompileError{Output: []byte("late but informative"), Err: errors.New("x")}
	shipCompileOutput(ctx, TriggerLoopOptions{LogsURL: ts.URL}, "run-z", buildErr, slog.Default())

	done := make(chan struct{})
	go func() { posted.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ship never POSTed despite cancelled parent ctx")
	}
}
