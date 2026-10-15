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
