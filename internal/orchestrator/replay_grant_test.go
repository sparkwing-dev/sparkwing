package orchestrator_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

type replayGrantPipe struct{ sparkwing.Base }

type replayGrantJob struct{ sparkwing.Base }

func (*replayGrantJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) error {
		// safety: asking the guard inside the body is what proves the replay
		// process makes a grant of its own, rather than inheriting one.
		planguard.Guard(ctx, "replay.probe")
		return nil
	}), nil
}

func (replayGrantPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "probe", &replayGrantJob{})
	return nil
}

func init() {
	register("orch-replay-grant", func() sparkwing.Pipeline[sparkwing.NoInputs] { return replayGrantPipe{} })
}

func TestRunReplayNode_GrantsTheContextItDispatchesWith(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-replay-grant"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); the replay needs a node that ran", res.Status, res.Error)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	replayID, err := orchestrator.MintReplayRun(ctx, st, res.RunID, "probe")
	if err != nil {
		t.Fatalf("MintReplayRun: %v", err)
	}
	// safety: a refused node comes back as a failed outcome rather than an
	// error, so the outcome is what holds the grant.
	res2, err := orchestrator.RunReplayNode(ctx, p, orchestrator.LocalBackends(p, st, nil), replayID, "probe", nil)
	if err != nil {
		t.Fatalf("RunReplayNode: %v", err)
	}
	if res2.Outcome != sparkwing.Success {
		t.Fatalf("replayed node outcome = %v (err=%v); the replay process must grant the context it dispatches with",
			res2.Outcome, res2.Err)
	}
}
