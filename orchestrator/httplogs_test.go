package orchestrator_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/v2/logs"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

type httpLogsPipe struct{ sparkwing.Base }

func (httpLogsPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {	a := sparkwing.Job(plan, "a", func(ctx context.Context) error {
		sparkwing.Info(ctx, "a: first line")
		sparkwing.Info(ctx, "a: second line")
		return nil
	})
	sparkwing.Job(plan, "b", func(ctx context.Context) error {
		sparkwing.Info(ctx, "b: only line")
		return nil
	}).Needs(a)
	return nil
}

// TestHTTPLogs_PipelineLogsReachService runs a real pipeline with
// HTTPLogs as the LogBackend and confirms every Log() call landed
// in the logs service's storage. This is the full "cluster-mode
// log routing" slice.
func TestHTTPLogs_PipelineLogsReachService(t *testing.T) {
	register("httplogs-demo", func() sparkwing.Pipeline[sparkwing.NoInputs] { return httpLogsPipe{} })

	dir := t.TempDir()

	// Stand up the logs service.
	logsRoot := filepath.Join(dir, "logs-root")
	logsSrvObj, err := logs.New(logsRoot, nil)
	if err != nil {
		t.Fatalf("logs.New: %v", err)
	}
	logsSrv := httptest.NewServer(logsSrvObj.Handler())
	defer logsSrv.Close()

	// Local store for State (this test focuses on logs, not State).
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	paths := orchestrator.PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}

	local := orchestrator.LocalBackends(paths, st)
	backends := orchestrator.Backends{
		State:       local.State,
		Logs:        orchestrator.NewHTTPLogs(logsSrv.URL, nil, nil),
		Concurrency: local.Concurrency,
	}

	res, err := orchestrator.Run(context.Background(), backends,
		orchestrator.Options{Pipeline: "httplogs-demo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status=%q want success", res.Status)
	}

	// Fetch what the logs service has for each node.
	client := logs.NewClient(logsSrv.URL, nil)

	gotA, err := client.Read(context.Background(), res.RunID, "a")
	if err != nil {
		t.Fatalf("Read a: %v", err)
	}
	if !bytes.Contains(gotA, []byte("a: first line")) ||
		!bytes.Contains(gotA, []byte("a: second line")) {
		t.Errorf("a logs missing content:\n%s", gotA)
	}

	gotB, err := client.Read(context.Background(), res.RunID, "b")
	if err != nil {
		t.Fatalf("Read b: %v", err)
	}
	if !bytes.Contains(gotB, []byte("b: only line")) {
		t.Errorf("b logs missing content:\n%s", gotB)
	}

	// Also verify the concat endpoint.
	run, err := client.ReadRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	for _, want := range []string{"=== a ===", "=== b ===", "first line", "only line"} {
		if !bytes.Contains(run, []byte(want)) {
			t.Errorf("ReadRun missing %q:\n%s", want, run)
		}
	}
}
