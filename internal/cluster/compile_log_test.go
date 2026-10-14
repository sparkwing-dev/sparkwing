package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func compileLogController(t *testing.T, runID string) (string, *client.Client) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: store.CompileLogNode, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, discardLogger()).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, client.New(srv.URL, nil)
}

func readCompileLog(t *testing.T, ctrl *client.Client, runID string) string {
	t.Helper()
	got, err := ctrl.NodeLogs().Read(t.Context(), runID, store.CompileLogNode, storage.ReadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func TestShipCompileOutput_ReachesTheControllerAsSegments(t *testing.T) {
	const runID = "run-test-imp-001"
	const want = "go: go.mod requires go >= 9.99.0\n./pipeline.go:7:2: undefined: Foo"
	url, ctrl := compileLogController(t, runID)

	compileLog := orchestrator.NewEngineNodeLog(url, "", runID, store.CompileLogNode, discardLogger())
	buildErr := &bincache.CompileError{Output: []byte(want), Err: errors.New("exit status 1")}
	shipCompileOutput(context.Background(), compileLog, runID, buildErr, discardLogger())
	compileLog.Close(context.Background())

	if got := readCompileLog(t, ctrl, runID); !strings.Contains(got, "undefined: Foo") {
		t.Errorf("compile log = %q, want the compiler output %q", got, want)
	}
}

func TestShipCompileOutput_NoControllerNoOp(t *testing.T) {
	buildErr := &bincache.CompileError{Output: []byte("oops"), Err: errors.New("x")}
	shipCompileOutput(context.Background(), orchestrator.NewEngineNodeLog("", "", "run-x", store.CompileLogNode, slog.Default()),
		"run-x", buildErr, slog.Default())
}

func TestShipCompileOutput_NonCompileErrorIgnored(t *testing.T) {
	var posted atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	shipCompileOutput(context.Background(), orchestrator.NewEngineNodeLog(ts.URL, "", "run-y", store.CompileLogNode, slog.Default()),
		"run-y", errors.New("hash failed"), slog.Default())
	if posted.Load() {
		t.Errorf("expected no request for non-CompileError; server saw one")
	}
}

func TestShipCompileOutput_LandsEvenWhenCtxCancelled(t *testing.T) {
	const runID = "run-z"
	url, ctrl := compileLogController(t, runID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	compileLog := orchestrator.NewEngineNodeLog(url, "", runID, store.CompileLogNode, discardLogger())
	buildErr := &bincache.CompileError{Output: []byte("late but informative"), Err: errors.New("x")}
	shipCompileOutput(ctx, compileLog, runID, buildErr, discardLogger())
	compileLog.Close(ctx)

	if got := readCompileLog(t, ctrl, runID); !strings.Contains(got, "late but informative") {
		t.Errorf("compile log = %q, want the output shipped under a cancelled context", got)
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
		entry: pipelineentry.Binary("/tmp/pipeline"),
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
