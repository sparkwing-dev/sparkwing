package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

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

func TestShipCompileOutput_NoLogsURLNoOp(t *testing.T) {
	buildErr := &bincache.CompileError{Output: []byte("oops"), Err: errors.New("x")}
	shipCompileOutput(context.Background(), TriggerLoopOptions{}, "run-x", buildErr, slog.Default())
}

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

func TestBinaryCacheOutcome(t *testing.T) {
	for _, test := range []struct {
		name     string
		fetched  bool
		compiled bool
		want     string
	}{
		{name: "already in this pod's cache", want: "local"},
		{name: "pulled from the gitcache", fetched: true, want: "remote"},
		{name: "built here", compiled: true, want: "compiled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := binaryCacheOutcome(test.fetched, test.compiled); got != test.want {
				t.Fatalf("binaryCacheOutcome(%v, %v) = %q, want %q", test.fetched, test.compiled, got, test.want)
			}
		})
	}
}

func TestLogBinaryReadyCarriesTheCompileMeasurement(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	logBinaryReady(logger, "run-42", triggerBinary{
		path:  "/tmp/pipeline",
		cache: binaryCacheCompiled,
		build: 187 * time.Second,
	})

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log record is not JSON (%v): %s", err, buf.String())
	}
	for key, want := range map[string]any{
		"run_id":       "run-42",
		"binary_cache": "compiled",
		"build_ms":     float64(187000),
	} {
		if got := record[key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}
