package main

import (
	"context"
	"encoding/json"
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

func TestRunJobsReceipt_ReadsAStandaloneRun(t *testing.T) {
	paths := standaloneHome(t)
	out := captureStdout(t, func() {
		if err := runJobsReceipt(context.Background(), paths, []string{"--run", "run-alone"}); err != nil {
			t.Fatalf("runJobsReceipt: %v", err)
		}
	})
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("decode receipt: %v\n%s", err, out)
	}
	if rec["run_id"] != "run-alone" {
		t.Fatalf("run_id = %v, want run-alone", rec["run_id"])
	}
	if rec["store"] != "standalone/state.db" {
		t.Fatalf("store = %v, want standalone/state.db", rec["store"])
	}
}

func TestRunsCancel_StandaloneRunNamesItsStore(t *testing.T) {
	paths := standaloneHome(t)
	out := captureStdout(t, func() {
		err := runRunsCancel(context.Background(), []string{"--run", "run-alone", "--home", paths.Root})
		if err == nil {
			t.Fatal("expected cancel to fail for a run in another store")
		}
	})
	if !strings.Contains(out, filepath.Join(paths.StandaloneDir(), "state.db")) {
		t.Fatalf("cancel did not name the standalone store:\n%s", out)
	}
	if !strings.Contains(out, "SPARKWING_HOME="+paths.StandaloneDir()) {
		t.Fatalf("cancel did not name the home that reaches the store:\n%s", out)
	}
	if strings.Contains(out, "not found") {
		t.Fatalf("cancel still answers not found:\n%s", out)
	}
}
