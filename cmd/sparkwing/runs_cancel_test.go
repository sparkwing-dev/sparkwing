package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunsCancelUsesLocalStoreWithoutDashboard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONTROLLER_URL", "")
	t.Setenv("SPARKWING_LOGS_URL", "")

	paths := orchestrator.Paths{Root: home}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.CreateRun(context.Background(), store.Run{
		ID:        "run-local-cancel",
		Pipeline:  "pipeline",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	var out bytes.Buffer
	if err := runRunsCancel(context.Background(), paths, &out, []string{"--run", "run-local-cancel"}); err != nil {
		t.Fatalf("runRunsCancel: %v", err)
	}
	if !strings.Contains(out.String(), "cancel: 1 ok, 0 failed") {
		t.Fatalf("output = %q, want success summary", out.String())
	}

	run, err := st.GetRun(context.Background(), "run-local-cancel")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled", run.Status)
	}
}
