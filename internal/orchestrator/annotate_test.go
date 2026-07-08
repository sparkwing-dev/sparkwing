package orchestrator_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type annotatingPipe struct{ sparkwing.Base }

func (annotatingPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error {
		sparkwing.Annotate(ctx, "foo")
		sparkwing.Annotate(ctx, "bar")
		return nil
	})
	return nil
}

func init() {
	register("orch-annotate", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &annotatingPipe{} })
}

// sparkwing.Annotate routing is disjoint: messages emitted inside a
// step body land on the step row, messages emitted between steps land
// on the node row. The func(ctx) error form passed to sparkwing.Job
// is implicitly wrapped in a single step named "run", so annotations
// fired from inside that closure land on that step's row.
func TestRun_AnnotatePersistsToStepRow(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-annotate"})
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
	defer func() { _ = st.Close() }()

	steps, err := st.ListNodeSteps(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("got %d steps, want 1", len(steps))
	}
	if steps[0].StepID != "run" {
		t.Fatalf("step id = %q, want %q", steps[0].StepID, "run")
	}
	got := steps[0].Annotations
	want := []string{"foo", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("annotations = %v, want %v", got, want)
	}

	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	if len(nodes[0].Annotations) != 0 {
		t.Fatalf("step-scoped annotations leaked onto node row: %v", nodes[0].Annotations)
	}
}
