package orchestrator_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestJobLogsRemoteReadsEventsWithoutALogsService(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := orchestrator.NewControllerServer(t, st, nil)
	// safety: discovery caches by URL for the process, and a reused test port would return another test's services.
	discovery.ResetCache()
	t.Cleanup(discovery.ResetCache)
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: "run-events", Pipeline: "demo", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(ctx, "run-events", "build", "node_started", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := orchestrator.JobLogsRemoteWithTokens(ctx, srv.URL, "", "", "run-events",
		orchestrator.LogsOpts{EventsOnly: true, JSON: true}, &out); err != nil {
		t.Fatalf("events-only read against a controller with no logs service: %v", err)
	}
	if !strings.Contains(out.String(), "node_started") {
		t.Fatalf("events-only output = %q, want the run's event", out.String())
	}

	err = orchestrator.JobLogsRemoteWithTokens(ctx, srv.URL, "", "", "run-events", orchestrator.LogsOpts{}, &out)
	if err == nil || !strings.Contains(err.Error(), "announces no logs service") {
		t.Fatalf("node-log read err = %v, want the missing logs service named", err)
	}
}
