package orchestrator_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// ciBuildJob mirrors the shape the public example pipeline uses: a
// fetch step, a GroupSteps "ci" cluster of three siblings, and a
// downstream compile step. It's intentionally tiny -- we're
// exercising the snapshot persistence + decoration path, not the
// example pipeline itself.
type ciBuildJob struct{ sparkwing.Base }

func (ciBuildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	fetch := sparkwing.Step(w, "fetch", func(ctx context.Context) error { return nil })
	lint := sparkwing.Step(w, "lint", func(ctx context.Context) error { return nil }).Needs(fetch)
	vet := sparkwing.Step(w, "vet", func(ctx context.Context) error { return nil }).Needs(fetch)
	test := sparkwing.Step(w, "test", func(ctx context.Context) error { return nil }).Needs(fetch)
	ci := sparkwing.GroupSteps(w, "ci", lint, vet, test)
	return sparkwing.Step(w, "compile", func(ctx context.Context) error { return nil }).Needs(ci), nil
}

type ciPipe struct{ sparkwing.Base }

func (ciPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", &ciBuildJob{})
	return nil
}

func init() {
	register("orch-ci-groups", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &ciPipe{} })
}

// TestRun_StepGroupsSurviveStoreToAPI runs a pipeline that declares
// a GroupSteps cluster, then reads the run back through the same
// path the controller uses to serve GET /api/v1/runs/{id}?include=
// nodes (store.GetRun + store.ListNodes + api.DecorateNodes). The
// decorated build node's work.step_groups must contain the cluster
// in declaration order.
func TestRun_StepGroupsSurviveStoreToAPI(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-ci-groups"})
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

	run, err := st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}

	steps, _ := st.ListNodeSteps(context.Background(), res.RunID)
	decorated := api.DecorateNodes(nodes, run.PlanSnapshot, steps, nil, nil)
	var build *api.NodeWithDecorations
	for _, n := range decorated {
		if n.NodeID == "build" {
			build = n
			break
		}
	}
	if build == nil {
		t.Fatalf("build node missing from decorated output")
	}
	if build.Decorations == nil || build.Decorations.Work == nil {
		t.Fatalf("Decorations.Work missing: %+v", build.Decorations)
	}
	groups := build.Decorations.Work.StepGroups
	if len(groups) != 1 {
		t.Fatalf("step_groups len=%d, want 1 (%+v)", len(groups), groups)
	}
	if groups[0].Name != "ci" {
		t.Errorf("step_groups[0].Name = %q, want %q", groups[0].Name, "ci")
	}
	want := []string{"lint", "vet", "test"}
	if !reflect.DeepEqual(groups[0].Members, want) {
		t.Errorf("step_groups[0].Members = %v, want %v", groups[0].Members, want)
	}
}
