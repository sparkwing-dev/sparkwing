package orchestrator_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type summaryNodePipe struct{ sparkwing.Base }

// Summary fires from BeforeRun so ctx carries WithNode but no
// WithStep -- exercises the node-scope branch of the wrapper.
// Plain sparkwing.Job(plan, id, fn) auto-wraps fn as Step("run"),
// which would put the summary on the step row instead.
func (summaryNodePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error {
		return nil
	}).
		BeforeRun(func(ctx context.Context) error {
			sparkwing.Summary(ctx, "## first")
			sparkwing.Summary(ctx, "## second")
			return nil
		})
	return nil
}

type summaryStepPipe struct{ sparkwing.Base }

func (summaryStepPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, &summaryStepJob{})
	return nil
}

type summaryStepJob struct{ sparkwing.Base }

func (j *summaryStepJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "deploy", func(ctx context.Context) error {
		sparkwing.Summary(ctx, "## step first")
		sparkwing.Summary(ctx, "## step second")
		return nil
	}), nil
}

func init() {
	register("orch-summary-node", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &summaryNodePipe{} })
	register("orch-summary-step", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &summaryStepPipe{} })
}

func TestRun_SummaryPersistsToNodeRow(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-summary-node"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	if got, want := nodes[0].Summary, "## second"; got != want {
		t.Fatalf("Summary = %q, want %q (overwrite-on-write)", got, want)
	}
}

func TestRun_SummaryPersistsToStepRow(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-summary-step"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	steps, err := st.ListNodeSteps(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	var deploy *store.NodeStep
	for _, s := range steps {
		if s.StepID == "deploy" {
			deploy = s
			break
		}
	}
	if deploy == nil {
		t.Fatalf("no deploy step in %d rows", len(steps))
	}
	if got, want := deploy.Summary, "## step second"; got != want {
		t.Fatalf("step Summary = %q, want %q (overwrite-on-write)", got, want)
	}

	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	if nodes[0].Summary != "" {
		t.Fatalf("node Summary = %q, want empty (step-scoped should not bubble)", nodes[0].Summary)
	}
}
