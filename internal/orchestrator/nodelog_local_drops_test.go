package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var (
	errLocalLogWrite = errors.New("no space left on device")
	errLocalLogClose = errors.New("close: input/output error")
)

type stubLogFile struct {
	buf      bytes.Buffer
	writeErr error
	closeErr error
	closes   int
}

func (f *stubLogFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.buf.Write(p)
}

func (f *stubLogFile) Close() error {
	f.closes++
	return f.closeErr
}

func newStubNodeLogger(nodeID string, f *stubLogFile) *nodeLogger {
	return &nodeLogger{file: f, enc: json.NewEncoder(f), nodeID: nodeID}
}

type stubLogs struct {
	mu   sync.Mutex
	open func(nodeID string) *nodeLogger
	logs []*nodeLogger
}

func (l *stubLogs) OpenNodeLog(_, nodeID string, _ sparkwing.Logger) (NodeLog, error) {
	nl := l.open(nodeID)
	l.mu.Lock()
	l.logs = append(l.logs, nl)
	l.mu.Unlock()
	return nl, nil
}

type localDropPipe struct{ sparkwing.Base }

func (localDropPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "only", func(ctx context.Context) error {
		sparkwing.Info(ctx, "doing the work")
		return nil
	})
	return nil
}

func dropRun(t *testing.T, pipeline string, open func(nodeID string) *nodeLogger) (*Result, *store.Store) {
	t.Helper()
	sparkwing.Register[sparkwing.NoInputs](pipeline,
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return localDropPipe{} })

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	paths := PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	backends := LocalBackends(paths, st, nil)
	backends.Logs = &stubLogs{open: open}

	res, err := Run(context.Background(), backends, Options{Pipeline: pipeline})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, st
}

func nodeRecord(t *testing.T, st *store.Store, runID, nodeID string) *store.Node {
	t.Helper()
	nodes, err := st.ListNodes(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for _, n := range nodes {
		if n.NodeID == nodeID {
			return n
		}
	}
	t.Fatalf("expected node %q in nodes list", nodeID)
	return nil
}

func TestNodeLoggerReportsLocalWriteFailuresAsDrops(t *testing.T) {
	nl := newStubNodeLogger("only", &stubLogFile{writeErr: errLocalLogWrite})
	for i := 0; i < 3; i++ {
		nl.Log("info", "a line the disk refused")
	}

	count, reason := nodeLogDrops(nl)
	if count != 3 {
		t.Errorf("nodeLogDrops count = %d, want 3 (one per line the file rejected)", count)
	}
	if !strings.Contains(reason, errLocalLogWrite.Error()) {
		t.Errorf("nodeLogDrops reason = %q, want the write error", reason)
	}
}

func TestNodeLoggerCloseFailureIsADrop(t *testing.T) {
	f := &stubLogFile{closeErr: errLocalLogClose}
	nl := newStubNodeLogger("only", f)
	nl.Log("info", "a line the file took")

	if err := nl.Close(); !errors.Is(err, errLocalLogClose) {
		t.Errorf("Close error = %v, want the close failure", err)
	}
	count, reason := nodeLogDrops(nl)
	if count != 1 || !strings.Contains(reason, errLocalLogClose.Error()) {
		t.Errorf("nodeLogDrops = (%d, %q), want the close failure counted once", count, reason)
	}

	if err := nl.Close(); err != nil {
		t.Errorf("second Close error = %v, want nil: the executor's deferred close must be a no-op", err)
	}
	if f.closes != 1 {
		t.Errorf("file closed %d times, want 1", f.closes)
	}
	if count, _ := nodeLogDrops(nl); count != 1 {
		t.Errorf("nodeLogDrops count after the second close = %d, want 1", count)
	}
}

func TestNodeLoggerMarshalFailureDropsOneLine(t *testing.T) {
	f := &stubLogFile{}
	nl := newStubNodeLogger("only", f)

	nl.Emit(sparkwing.LogRecord{Level: "info", Msg: "unencodable", Attrs: map[string]any{"n": math.NaN()}})
	count, reason := nodeLogDrops(nl)
	if count != 1 {
		t.Errorf("nodeLogDrops count = %d, want 1 for the record that would not encode", count)
	}
	if !strings.Contains(reason, "NaN") {
		t.Errorf("nodeLogDrops reason = %q, want the encoding failure", reason)
	}

	nl.Log("info", "the next line still lands")
	if !strings.Contains(f.buf.String(), "the next line still lands") {
		t.Errorf("a record that would not encode must not stop the writer, got: %q", f.buf.String())
	}
	if count, _ := nodeLogDrops(nl); count != 1 {
		t.Errorf("nodeLogDrops count = %d after a good line, want 1", count)
	}
}

func TestHTTPNodeLogMarshalFailureDropsOneLine(t *testing.T) {
	var mu sync.Mutex
	var appends int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			appends++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	quiet := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	nlog, err := NewHTTPLogs(srv.URL, nil, quiet).OpenNodeLog("run-1", "only", nil)
	if err != nil {
		t.Fatal(err)
	}

	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "unencodable", Attrs: map[string]any{"n": math.NaN()}})
	count, reason := nodeLogDrops(nlog)
	if count != 1 {
		t.Errorf("nodeLogDrops count = %d, want 1: the remote writer must count an unencodable record too", count)
	}
	if !strings.Contains(reason, "NaN") {
		t.Errorf("nodeLogDrops reason = %q, want the encoding failure", reason)
	}
	if fatal := nodeLogFatal(nlog); fatal != nil {
		t.Errorf("nodeLogFatal = %v, want nil: an unencodable record is a dropped line, not a fatal", fatal)
	}

	nlog.Log("info", "the next line still ships")
	mu.Lock()
	got := appends
	mu.Unlock()
	if got != 1 {
		t.Errorf("appends = %d, want 1 (the unencodable record is dropped, the good one ships)", got)
	}
}

func TestLocalLogWriteFailure_FailsNodeInsteadOfReportingSuccess(t *testing.T) {
	res, st := dropRun(t, "localdrop-demo", func(nodeID string) *nodeLogger {
		return newStubNodeLogger(nodeID, &stubLogFile{writeErr: errLocalLogWrite})
	})
	if res.Status != "failed" {
		t.Errorf("Status: got %q, want failed (log lines lost on disk must not report success)", res.Status)
	}

	n := nodeRecord(t, st, res.RunID, "only")
	if n.FailureReason != store.FailureLogsDropped {
		t.Errorf("FailureReason: got %q, want %q", n.FailureReason, store.FailureLogsDropped)
	}
	if !strings.Contains(n.Error, errLocalLogWrite.Error()) {
		t.Errorf("Node.Error should name the write failure, got: %q", n.Error)
	}
}

func TestLocalLogCloseFailure_FailsNodeInsteadOfReportingSuccess(t *testing.T) {
	res, st := dropRun(t, "localdropclose-demo", func(nodeID string) *nodeLogger {
		return newStubNodeLogger(nodeID, &stubLogFile{closeErr: errLocalLogClose})
	})
	if res.Status != "failed" {
		t.Errorf("Status: got %q, want failed (a log file that will not close lost lines)", res.Status)
	}

	n := nodeRecord(t, st, res.RunID, "only")
	if n.FailureReason != store.FailureLogsDropped {
		t.Errorf("FailureReason: got %q, want %q", n.FailureReason, store.FailureLogsDropped)
	}
	if !strings.Contains(n.Error, errLocalLogClose.Error()) {
		t.Errorf("Node.Error should name the close failure, got: %q", n.Error)
	}

	events, err := st.ListEventsAfter(context.Background(), res.RunID, 0, 0)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	var drops int
	for _, e := range events {
		if e.Kind == "logs_drop" {
			drops++
		}
	}
	if drops != 1 {
		t.Errorf("logs_drop events = %d, want exactly 1", drops)
	}
}

func TestLocalLogWriteFailure_WarnPolicyKeepsRunGreen(t *testing.T) {
	t.Setenv(LogsDropPolicyEnvVar, "warn")
	res, _ := dropRun(t, "localdropwarn-demo", func(nodeID string) *nodeLogger {
		return newStubNodeLogger(nodeID, &stubLogFile{writeErr: errLocalLogWrite})
	})
	if res.Status != "success" {
		t.Errorf("Status: got %q, want success under %s=warn", res.Status, LogsDropPolicyEnvVar)
	}
}

func TestLocalLogCloseFailure_WarnPolicyKeepsRunGreen(t *testing.T) {
	t.Setenv(LogsDropPolicyEnvVar, "warn")
	res, _ := dropRun(t, "localdropclosewarn-demo", func(nodeID string) *nodeLogger {
		return newStubNodeLogger(nodeID, &stubLogFile{closeErr: errLocalLogClose})
	})
	if res.Status != "success" {
		t.Errorf("Status: got %q, want success under %s=warn", res.Status, LogsDropPolicyEnvVar)
	}
}
