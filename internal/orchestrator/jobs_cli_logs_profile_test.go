package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedProfileRunWithNode(t *testing.T, dbPath, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	seed, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	defer func() { _ = seed.Close() }()
	if err := seed.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatalf("seed CreateRun: %v", err)
	}
	if err := seed.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("seed CreateNode: %v", err)
	}
	if err := seed.StartNode(ctx, runID, nodeID); err != nil {
		t.Fatalf("seed StartNode: %v", err)
	}
}

func TestJobLogs_ListsNodesFromProfileState(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "profile-state.db")
	const runID, nodeID = "run-profile-logs", "n1"
	seedProfileRunWithNode(t, dbPath, runID, nodeID)

	paths := Paths{Root: t.TempDir()}
	if err := paths.EnsureRunDir(runID); err != nil {
		t.Fatalf("EnsureRunDir: %v", err)
	}
	if err := os.WriteFile(paths.NodeLog(runID, nodeID), []byte("hello from n1\n"), 0o600); err != nil {
		t.Fatalf("write node log: %v", err)
	}

	p := &profile.Profile{Name: "prod", State: &backends.Spec{Type: backends.TypeSQLite, Path: dbPath}}
	var buf bytes.Buffer
	if err := JobLogs(ctx, paths, runID, LogsOpts{Profile: p, Node: nodeID, Format: "plain"}, &buf); err != nil {
		t.Fatalf("JobLogs: %v", err)
	}
	if !strings.Contains(buf.String(), "hello from n1") {
		t.Fatalf("logs did not come from the profile's state; output:\n%s", buf.String())
	}
}

func TestJobLogsTree_ReadsProfileState(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "profile-state.db")
	const runID, nodeID = "run-profile-tree", "n1"
	seedProfileRunWithNode(t, dbPath, runID, nodeID)

	paths := Paths{Root: t.TempDir()}
	if err := paths.EnsureRunDir(runID); err != nil {
		t.Fatalf("EnsureRunDir: %v", err)
	}
	if err := os.WriteFile(paths.NodeLog(runID, nodeID), []byte("tree line\n"), 0o600); err != nil {
		t.Fatalf("write node log: %v", err)
	}

	p := &profile.Profile{Name: "prod", State: &backends.Spec{Type: backends.TypeSQLite, Path: dbPath}}
	var buf bytes.Buffer
	if err := JobLogs(ctx, paths, runID, LogsOpts{Profile: p, Tree: true, Format: "plain"}, &buf); err != nil {
		t.Fatalf("JobLogs --tree: %v", err)
	}
	if !strings.Contains(buf.String(), "tree line") {
		t.Fatalf("--tree did not read the profile's state; output:\n%s", buf.String())
	}
}
