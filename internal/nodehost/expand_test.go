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

const testGenerator = "targets/generator/0"

func fanOutDoc() nodeproto.PlanDocOld {
	return nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{
		{ID: "targets", Closures: &nodeproto.PlanClosures{Generator: []string{testGenerator}}},
		{ID: "summary", Deps: deps("lint"), NeedsGenerated: []string{testGenerator}},
		{ID: "lint"},
	}}
}

func generates(nodes ...nodeproto.PlanNode) Executor {
	return func(context.Context, *NodeRun) Result {
		return Result{Outcome: sparkwing.Success, expansions: []expansion{{closure: testGenerator, nodes: nodes}}}
	}
}

func TestExpansionFansOutAndHoldsItsWaiter(t *testing.T) {
	st := openStore(t)
	var children []nodeproto.PlanNode
	for i := range 5 {
		children = append(children, nodeproto.PlanNode{ID: fmt.Sprintf("test-%d", i), Deps: deps("targets")})
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
	if want := []string{"lint", "test-0", "test-1", "test-2", "test-3", "test-4"}; !slices.Equal(row.Deps, want) {
		t.Errorf("summary row deps = %v, want %v", row.Deps, want)
	}
}

func TestExpansionFailureCancelsItsWaiter(t *testing.T) {
	child := nodeproto.PlanNode{ID: "test-a", Deps: deps("targets")}
	cases := []struct {
		name   string
		source Executor
		errHas string
	}{
		{"GeneratorError", func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Success, expansions: []expansion{{closure: testGenerator, err: errors.New("generator panicked")}}}
		}, "expansion failed: generator panicked"},
		{"SourceFailed", func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("broke"), expansions: []expansion{{closure: testGenerator, nodes: []nodeproto.PlanNode{child}}}}
		}, `expansion source "targets" did not succeed (outcome=failed)`},
		{"SourceSkippedTheGenerator", func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Skipped}
		}, "generator targets/generator/0 did not run"},
		{"DuplicateID", generates(child, nodeproto.PlanNode{ID: "lint", Deps: deps("targets")}), "the plan holds node lint twice"},
		{"DuplicateWithinTheBatch", generates(child, child), "the plan holds node test-a twice"},
		{"NodeThatSkipsItsSource", generates(child, nodeproto.PlanNode{ID: "test-b"}), "does not need its source targets"},
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
	child := nodeproto.PlanNode{ID: "test-a", Deps: deps("targets")}
	fx := &fakeExec{bodies: map[string]Executor{"targets": func(context.Context, *NodeRun) Result {
		cancel()
		return Result{Outcome: sparkwing.Success, expansions: []expansion{{closure: testGenerator, nodes: []nodeproto.PlanNode{child}}}}
	}}}
	doc := fanOutDoc()
	doc.Nodes[1].Deps = nil

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
	doc.Nodes[1].NeedsGenerated = []string{"elsewhere/generator/0"}
	if err := validateNodes(doc.Nodes); err == nil || !strings.Contains(err.Error(), "generator elsewhere/generator/0, which the plan does not hold") {
		t.Errorf("unknown generator: err = %v", err)
	}
	doc = fanOutDoc()
	doc.Nodes[0].Deps = deps("summary")
	if err := validateNodes(doc.Nodes); err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Errorf("a source that needs its own waiter: err = %v", err)
	}
}
