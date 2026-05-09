package orchestrator_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type authFailPipe struct{ sparkwing.Base }

func (authFailPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "only", func(ctx context.Context) error {
		// User code "succeeds" but every Emit silently fails on 403
		// in the unfixed world; with the auth-fatal path wired in the
		// run still ends up failed because the auth error is fatal.
		sparkwing.Info(ctx, "doing the work")
		return nil
	})
	return nil
}

// When logs.append returns 403 to the runner, the run must hard-fail
// with a structured error rather than report status=success with
// empty logs. This is the single most important behavioral
// guarantee.
func TestHTTPLogs_403HardFailsRun(t *testing.T) {
	register("authfail-demo", func() sparkwing.Pipeline[sparkwing.NoInputs] { return authFailPipe{} })

	var hits atomic.Int64
	logsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// All POSTs to log endpoints get 403.
		if r.Method == http.MethodPost {
			hits.Add(1)
			http.Error(w, "token lacks required scope: logs.write", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer logsSrv.Close()

	dir := t.TempDir()
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
		orchestrator.Options{Pipeline: "authfail-demo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("Status: got %q, want failed (auth-blocked logs.append must abort the run)", res.Status)
	}
	// The dispatcher aggregates Run.Error to "nodes failed: [...]";
	// the actionable detail lives on the failing node row, where
	// `runs status` reads it for the per-node table. Pin both so
	// the auth message + structured FailureReason both land.
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
		if !strings.Contains(n.Error, "logs append blocked") {
			t.Errorf("Node.Error should mention auth block, got: %q", n.Error)
		}
		if !strings.Contains(n.Error, "logs.write") {
			t.Errorf("Node.Error should name the missing scope, got: %q", n.Error)
		}
		if n.FailureReason != store.FailureLogsAuth {
			t.Errorf("FailureReason: got %q, want %q", n.FailureReason, store.FailureLogsAuth)
		}
	}
	if !saw {
		t.Fatalf("expected node 'only' in nodes list")
	}
	if hits.Load() == 0 {
		t.Errorf("expected at least one POST to the logs server")
	}
}
