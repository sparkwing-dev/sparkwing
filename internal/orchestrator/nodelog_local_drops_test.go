package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var errLocalLogWrite = errors.New("no space left on device")

type failingLogFile struct{ closed bool }

func (f *failingLogFile) Write([]byte) (int, error) { return 0, errLocalLogWrite }

func (f *failingLogFile) Close() error {
	f.closed = true
	return nil
}

func newFailingNodeLogger(nodeID string) *nodeLogger {
	f := &failingLogFile{}
	return &nodeLogger{file: f, enc: json.NewEncoder(f), nodeID: nodeID}
}

func TestNodeLoggerReportsLocalWriteFailuresAsDrops(t *testing.T) {
	nl := newFailingNodeLogger("only")
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

type failingLocalLogs struct {
	mu   sync.Mutex
	logs []*nodeLogger
}

func (l *failingLocalLogs) OpenNodeLog(_, nodeID string, _ sparkwing.Logger) (NodeLog, error) {
	nl := newFailingNodeLogger(nodeID)
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

func TestLocalLogWriteFailure_FailsNodeInsteadOfReportingSuccess(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("localdrop-demo",
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
	backends.Logs = &failingLocalLogs{}

	res, err := Run(context.Background(), backends, Options{Pipeline: "localdrop-demo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("Status: got %q, want failed (log lines lost on disk must not report success)", res.Status)
	}

	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	var saw bool
	for _, n := range nodes {
		if n.NodeID != "only" {
			continue
		}
		saw = true
		if n.FailureReason != store.FailureLogsDropped {
			t.Errorf("FailureReason: got %q, want %q", n.FailureReason, store.FailureLogsDropped)
		}
		if !strings.Contains(n.Error, errLocalLogWrite.Error()) {
			t.Errorf("Node.Error should name the write failure, got: %q", n.Error)
		}
	}
	if !saw {
		t.Fatalf("expected node 'only' in nodes list")
	}
}

func TestLocalLogWriteFailure_WarnPolicyKeepsRunGreen(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("localdropwarn-demo",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return localDropPipe{} })
	t.Setenv(LogsDropPolicyEnvVar, "warn")

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
	backends.Logs = &failingLocalLogs{}

	res, err := Run(context.Background(), backends, Options{Pipeline: "localdropwarn-demo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Errorf("Status: got %q, want success under %s=warn", res.Status, LogsDropPolicyEnvVar)
	}
}
