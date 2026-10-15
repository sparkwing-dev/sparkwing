package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func standaloneHome(t *testing.T) orchestrator.Paths {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SPARKWING_HOME", root)
	t.Setenv("SPARKWING_WINGD_BIN", "")
	t.Setenv("SPARKWING_CONTROLLER_URL", "")
	t.Setenv("PATH", t.TempDir())
	paths := orchestrator.PathsAt(root)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	writeRun(t, paths.StateDB(), "run-shared")
	writeRun(t, paths.StandaloneStateDB(), "run-alone")
	return paths
}

func writeRun(t *testing.T, path, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	err = st.CreateRun(context.Background(), store.Run{
		ID: id, Pipeline: "deploy", Status: "success",
		StartedAt: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunsRetry_StandaloneRunReachesTheRefusalWithNoDashboard(t *testing.T) {
	standaloneHome(t)
	err := runRunsRetry(context.Background(), []string{"--run", "run-alone", "--failed"})
	if err == nil {
		t.Fatal("expected retry to fail for a standalone run")
	}
	if !strings.Contains(err.Error(), "retry: 1 of 1 failed") {
		t.Fatalf("retry did not count the standalone run as its own failure: %v", err)
	}
	if strings.Contains(err.Error(), "no local dashboard running") {
		t.Fatalf("retry exited on the client before reaching the standalone sweep: %v", err)
	}
}

func TestRunsAnnotate_WritesIntoTheStandaloneStore(t *testing.T) {
	paths := standaloneHome(t)
	writeNode(t, paths.StandaloneStateDB(), "run-alone", "n1")

	err := addLocalAnnotation(context.Background(), paths, "run-alone", "n1", "", "from the test")
	if err != nil {
		t.Fatalf("annotate a standalone run: %v", err)
	}

	st, err := store.OpenReadOnly(paths.StandaloneStateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	node, err := st.GetNode(context.Background(), "run-alone", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(node.Annotations) != 1 || node.Annotations[0] != "from the test" {
		t.Fatalf("annotation did not land in the standalone store: %v", node.Annotations)
	}
}

func TestRunsAnnotationsList_ReadsTheStandaloneStore(t *testing.T) {
	paths := standaloneHome(t)
	writeNode(t, paths.StandaloneStateDB(), "run-alone", "n1")
	err := addLocalAnnotation(context.Background(), paths, "run-alone", "n1", "", "from the test")
	if err != nil {
		t.Fatalf("annotate: %v", err)
	}

	entries, err := listLocalAnnotations(context.Background(), paths, "run-alone", "", "", false)
	if err != nil {
		t.Fatalf("listLocalAnnotations: %v", err)
	}
	if len(entries) != 1 || entries[0].Message != "from the test" {
		t.Fatalf("annotations list did not read the standalone store: %+v", entries)
	}
}

func TestRunsApprovalsList_ReadsTheStandaloneStore(t *testing.T) {
	paths := standaloneHome(t)
	writeNode(t, paths.StandaloneStateDB(), "run-alone", "gate")
	st, err := store.Open(paths.StandaloneStateDB())
	if err != nil {
		t.Fatal(err)
	}
	err = st.CreateApproval(context.Background(), store.Approval{
		RunID: "run-alone", NodeID: "gate", RequestedAt: time.Now().UTC(), Message: "approve to ship",
	})
	if cerr := st.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	rows, err := listLocalApprovals(context.Background(), paths, "run-alone")
	if err != nil {
		t.Fatalf("listLocalApprovals: %v", err)
	}
	if len(rows) != 1 || rows[0].NodeID != "gate" {
		t.Fatalf("approvals list did not read the standalone store: %+v", rows)
	}
}

func TestRunsAnnotate_UnreadableStandaloneStoreIsNamed(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	paths := standaloneHome(t)
	aged := filepath.Join(paths.StandaloneSchemaDir(20), "state.db")
	writeRun(t, aged, "run-old")
	dropRunsColumn(t, aged, "last_heartbeat_at")

	stderr := captureStderr(t, func() {
		err := addLocalAnnotation(context.Background(), paths, "run-old", "n1", "", "nope")
		if err == nil {
			t.Fatal("expected the write to fail for a run in a store this build cannot read")
		}
	})
	if !strings.Contains(stderr, "standalone/schema-20/state.db") {
		t.Fatalf("the skipped store was not named:\n%s", stderr)
	}
	if !strings.Contains(stderr, "older sparkwing") {
		t.Fatalf("the note did not say why the store was skipped:\n%s", stderr)
	}
}

func dropRunsColumn(t *testing.T, path, column string) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB().Exec("ALTER TABLE runs DROP COLUMN " + column)
	if cerr := st.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatalf("drop %s: %v", column, err)
	}
}

func writeNode(t *testing.T, path, runID, nodeID string) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	err = st.CreateNode(context.Background(), store.Node{RunID: runID, NodeID: nodeID, Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
}

func writeFailedNode(t *testing.T, path, runID, nodeID, msg string) {
	t.Helper()
	writeNode(t, path, runID, nodeID)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, err = st.DB().ExecContext(context.Background(),
		`UPDATE nodes SET status='done', outcome='failed', error=? WHERE run_id=? AND node_id=?`,
		msg, runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
}
