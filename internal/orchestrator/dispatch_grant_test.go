package orchestrator_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

var dispatchProbeRan atomic.Bool

type dispatchGrantPipe struct{ sparkwing.Base }

type dispatchGrantJob struct{ sparkwing.Base }

func (*dispatchGrantJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) error {
		// safety: asking the guard inside the body is what proves the
		// dispatching process grants on its own rather than inheriting.
		planguard.Guard(ctx, "dispatch.probe")
		dispatchProbeRan.Store(true)
		return nil
	}), nil
}

func (dispatchGrantPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "probe", &dispatchGrantJob{})
	return nil
}

func init() {
	register("orch-dispatch-grant", func() sparkwing.Pipeline[sparkwing.NoInputs] { return dispatchGrantPipe{} })
}

func TestRunNodeOnce_GrantsTheContextItDispatchesWith(t *testing.T) {
	isolateProfiles(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	quiet := slog.New(slog.NewTextHandler(&syncBuffer{}, nil))
	srv := httptest.NewServer(controller.New(st, quiet).Handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const runID, nodeID = "run-dispatch-grant", "probe"
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "orch-dispatch-grant", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	dispatchProbeRan.Store(false)
	res, err := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, nodeID,
		"pod:"+runID+":"+nodeID, "", nil, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Success {
		t.Fatalf("node outcome = %q (err=%v); the dispatching process must grant the context it runs with",
			res.Outcome, res.Err)
	}
	// safety: a success reported without running the body would pass the
	// outcome check while proving nothing about the grant.
	if !dispatchProbeRan.Load() {
		t.Fatal("the node body never ran, so the outcome says nothing about the grant")
	}
}
