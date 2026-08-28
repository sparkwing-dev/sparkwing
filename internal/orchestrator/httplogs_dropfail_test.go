package orchestrator_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type dropFailPipe struct{ sparkwing.Base }

func (dropFailPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "only", func(ctx context.Context) error {
		sparkwing.Info(ctx, "doing the work")
		return nil
	})
	return nil
}

func dropFailBackends(t *testing.T, logsURL string) (orchestrator.Backends, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	paths := orchestrator.PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	local := orchestrator.LocalBackends(paths, st, nil)
	return orchestrator.Backends{
		State:       local.State,
		Logs:        orchestrator.NewHTTPLogs(logsURL, nil, nil),
		Concurrency: local.Concurrency,
	}, st
}

func alwaysFailingLogsServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestLogsDropped_FailsRunAndRecordsCount(t *testing.T) {
	register("dropfail-demo", func() sparkwing.Pipeline[sparkwing.NoInputs] { return dropFailPipe{} })
	orchestrator.SetTestHTTPNodeLogRetry(t, 2, 1)
	orchestrator.SetTestHTTPNodeLogDropCooldown(t, 0)

	backends, st := dropFailBackends(t, alwaysFailingLogsServer(t))
	res, err := orchestrator.Run(context.Background(), backends,
		orchestrator.Options{Pipeline: "dropfail-demo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("Status: got %q, want failed (lost log lines must not report success)", res.Status)
	}

	ctx := context.Background()
	nodes, err := st.ListNodes(ctx, res.RunID)
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
		for _, want := range []string{"log line(s) lost", "check:", "SPARKWING_LOGS_DROP_POLICY=warn", "cause:"} {
			if !strings.Contains(n.Error, want) {
				t.Errorf("Node.Error should contain %q, got: %q", want, n.Error)
			}
		}

		if strings.Index(n.Error, "cause:") < strings.Index(n.Error, "check:") {
			t.Errorf("the remedy should precede the store's error, got: %q", n.Error)
		}
	}
	if !saw {
		t.Fatalf("expected node 'only' in nodes list")
	}

	events, err := st.ListEventsAfter(ctx, res.RunID, 0, 0)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	var dropPayload string
	for _, e := range events {
		if e.Kind == "logs_drop" {
			dropPayload = string(e.Payload)
		}
	}
	if dropPayload == "" {
		t.Fatalf("no logs_drop event recorded; the drop count must reach the run record")
	}
	if !strings.Contains(dropPayload, `"count"`) {
		t.Errorf("logs_drop payload should carry the count, got: %q", dropPayload)
	}
}

func TestLogsDropped_WarnPolicyKeepsRunGreen(t *testing.T) {
	register("dropwarn-demo", func() sparkwing.Pipeline[sparkwing.NoInputs] { return dropFailPipe{} })
	orchestrator.SetTestHTTPNodeLogRetry(t, 2, 1)
	orchestrator.SetTestHTTPNodeLogDropCooldown(t, 0)
	t.Setenv(orchestrator.LogsDropPolicyEnvVar, "warn")

	backends, _ := dropFailBackends(t, alwaysFailingLogsServer(t))
	res, err := orchestrator.Run(context.Background(), backends,
		orchestrator.Options{Pipeline: "dropwarn-demo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Errorf("Status: got %q, want success under %s=warn", res.Status, orchestrator.LogsDropPolicyEnvVar)
	}
}

func TestLogsDropped_MisspelledPolicyStillFails(t *testing.T) {
	register("droptypo-demo", func() sparkwing.Pipeline[sparkwing.NoInputs] { return dropFailPipe{} })
	orchestrator.SetTestHTTPNodeLogRetry(t, 2, 1)
	orchestrator.SetTestHTTPNodeLogDropCooldown(t, 0)
	t.Setenv(orchestrator.LogsDropPolicyEnvVar, "warning")

	backends, _ := dropFailBackends(t, alwaysFailingLogsServer(t))
	res, _ := orchestrator.Run(context.Background(), backends,
		orchestrator.Options{Pipeline: "droptypo-demo"})
	if res.Status != "failed" {
		t.Errorf("Status: got %q, want failed (only the exact value \"warn\" opts out)", res.Status)
	}
}

func TestLogsDropped_404NamesTheMissingService(t *testing.T) {
	register("drop404-demo", func() sparkwing.Pipeline[sparkwing.NoInputs] { return dropFailPipe{} })
	orchestrator.SetTestHTTPNodeLogRetry(t, 2, 1)
	orchestrator.SetTestHTTPNodeLogDropCooldown(t, 0)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	backends, st := dropFailBackends(t, srv.URL)
	res, err := orchestrator.Run(context.Background(), backends,
		orchestrator.Options{Pipeline: "drop404-demo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for _, n := range nodes {
		if n.NodeID != "only" {
			continue
		}
		if !strings.Contains(n.Error, "separate service") {
			t.Errorf("a 404 should name the missing logs service, got: %q", n.Error)
		}
	}
}
