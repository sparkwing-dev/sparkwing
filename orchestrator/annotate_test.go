package orchestrator_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
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

func TestRun_AnnotatePersistsToNodeRow(t *testing.T) {
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
	defer st.Close()

	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	got := nodes[0].Annotations
	want := []string{"foo", "bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("annotations = %v, want %v", got, want)
	}
}
