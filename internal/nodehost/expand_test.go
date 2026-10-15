package nodehost

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const testGenerator = "targets/generate/0"

func fanOutDoc() nodeproto.PlanDoc {
	return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "targets", Closures: nodeproto.ClosureSpec{Generate: []string{testGenerator}}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "summary", Needs: deps("lint"), NeedsGenerated: []string{testGenerator}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "lint", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}
}

func generates(nodes ...nodeproto.JobSpec) Executor {
	return func(context.Context, *NodeRun) Result {
		return Result{Outcome: sparkwing.Success, expansions: []expansion{{closure: testGenerator, nodes: nodes}}}
	}
}

func TestExpansionFansOutAndHoldsItsWaiter(t *testing.T) {
	st := openStore(t)
	var children []nodeproto.JobSpec
	for i := range 5 {
		children = append(children, nodeproto.JobSpec{ID: fmt.Sprintf("test-%d", i), Needs: deps("targets"), Steps: []nodeproto.StepSpec{{ID: "body"}}})
	}
	fx := &fakeExec{bodies: map[string]Executor{"targets": generates(children...)}}

	res := hostDoc(t, t.Context(), st, fanOutDoc(), fx.run)

	if res.Status != "success" {
		t.Fatalf("status=%s err=%v, want success", res.Status, res.Err)
	}
	last := slices.Index(res.Order, "summary")
	for _, c := range children {
		wantRow(t, st, c.ID, sparkwing.Success, "")
		if i := slices.Index(res.Order, c.ID); i < 0 || i > last {
			t.Errorf("order %v: %s did not run before summary", res.Order, c.ID)
		}
		if got := fx.seen[c.ID].Generators; !slices.Equal(got, []string{testGenerator}) {
			t.Errorf("%s ran with generator %q, want %q", c.ID, got, testGenerator)
		}
	}
	if got := fx.seen["summary"].Generators; len(got) != 0 {
		t.Errorf("summary ran with generator %q, want none", got)
	}
	row := nodeRow(t, st, "summary")
	if want := []string{"lint"}; !slices.Equal(row.Deps, want) {
		t.Errorf("summary row deps = %v, want %v", row.Deps, want)
	}
}

func TestExpansionFailureCancelsItsWaiter(t *testing.T) {
	child := nodeproto.JobSpec{ID: "test-a", Needs: deps("targets"), Steps: []nodeproto.StepSpec{{ID: "body"}}}
	cases := []struct {
		name   string
		source Executor
		errHas string
	}{
		{"GeneratorError", func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Success, expansions: []expansion{{closure: testGenerator, err: errors.New("generator panicked")}}}
		}, "expansion failed: generator panicked"},
		{"SourceFailed", func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("broke"), expansions: []expansion{{closure: testGenerator, nodes: []nodeproto.JobSpec{child}}}}
		}, `expansion source "targets" did not succeed (outcome=failed)`},
		{"SourceSkippedTheGenerator", func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Skipped}
		}, "generator targets/generate/0 did not run"},
		{"DuplicateID", generates(child, nodeproto.JobSpec{ID: "lint", Needs: deps("targets"), Steps: []nodeproto.StepSpec{{ID: "body"}}}), "the plan holds node lint twice"},
		{"DuplicateWithinTheBatch", generates(child, child), "the plan holds node test-a twice"},
		{"NodeThatSkipsItsSource", generates(child, nodeproto.JobSpec{ID: "test-b", Steps: []nodeproto.StepSpec{{ID: "body"}}}), "does not need its source targets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openStore(t)
			fx := &fakeExec{bodies: map[string]Executor{"targets": tc.source}}
			res := hostDoc(t, t.Context(), st, fanOutDoc(), fx.run)
			if res.Status != "failed" {
				t.Errorf("status=%s err=%v, want failed", res.Status, res.Err)
			}
			wantRow(t, st, "summary", sparkwing.Cancelled, tc.errHas)
			if fx.ran("summary") || fx.ran("test-a") || fx.ran("test-b") {
				t.Errorf("ran %v, want neither summary nor a generated node", fx.calls)
			}
			if !fx.ran("lint") || len(fx.calls) != 2 {
				t.Errorf("ran %v, want targets and lint only", fx.calls)
			}
			if _, err := st.GetNode(t.Context(), testRunID, "test-a"); err == nil {
				t.Error("a refused generated node got a row")
			}
		})
	}
}

func TestExpansionDuringCancellationCancelsTheGeneratedNodes(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	child := nodeproto.JobSpec{ID: "test-a", Needs: deps("targets"), Steps: []nodeproto.StepSpec{{ID: "body"}}}
	fx := &fakeExec{bodies: map[string]Executor{"targets": func(context.Context, *NodeRun) Result {
		cancel()
		return Result{Outcome: sparkwing.Success, expansions: []expansion{{closure: testGenerator, nodes: []nodeproto.JobSpec{child}}}}
	}}}
	doc := fanOutDoc()
	doc.Jobs[1].Needs = nil

	res := hostDoc(t, ctx, st, doc, fx.run)

	if res.Status != "failed" {
		t.Errorf("status=%s err=%v, want failed", res.Status, res.Err)
	}
	wantRow(t, st, "test-a", sparkwing.Cancelled, "ctx-cancelled")
	wantRow(t, st, "summary", sparkwing.Cancelled, "ctx-cancelled")
	if fx.ran("test-a") || fx.ran("summary") {
		t.Errorf("ran %v after the run was cancelled", fx.calls)
	}
}

func TestValidateNodesChecksGeneratedEdges(t *testing.T) {
	doc := fanOutDoc()
	doc.Jobs[1].NeedsGenerated = []string{"elsewhere/generate/0"}
	if err := validateNodes(doc.Jobs, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}); err == nil || !strings.Contains(err.Error(), "generator is absent or self-referential") {
		t.Errorf("unknown generator: err = %v", err)
	}
	doc = fanOutDoc()
	doc.Jobs[0].Needs = deps("summary")
	if err := validateNodes(doc.Jobs, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}); err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Errorf("a source that needs its own waiter: err = %v", err)
	}
}

func TestGeneratedWaitsUsesMembershipWithoutRewritingDeclarations(t *testing.T) {
	job := nodeproto.JobSpec{ID: "summary", Needs: []string{"lint"}, NeedsGenerated: []string{"group"}}
	s := scheduler{}
	if _, ready, err := s.generatedWaits(job, slices.Clone(job.Needs)); err != nil || ready {
		t.Fatalf("unresolved group: ready=%t, %v", ready, err)
	}
	s.groups = map[string]*genGroup{"group": {done: true, members: []string{"first", "second"}}}
	waits, ready, err := s.generatedWaits(job, slices.Clone(job.Needs))
	if err != nil || !ready || !slices.Equal(waits, []string{"lint", "first", "second"}) || !slices.Equal(job.Needs, []string{"lint"}) {
		t.Fatalf("generated waits: %v, ready=%t, declaration=%v, %v", waits, ready, job.Needs, err)
	}
	s.groups["group"].members = nil
	if waits, ready, err := s.generatedWaits(job, slices.Clone(job.Needs)); err != nil || !ready || !slices.Equal(waits, job.Needs) {
		t.Fatalf("empty group: %v, ready=%t, %v", waits, ready, err)
	}
}
