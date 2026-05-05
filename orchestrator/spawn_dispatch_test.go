package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestSpawnDispatch_* tests SDK-002 PR3: a node's Work declares
// SpawnNode / SpawnNodeForEach; the orchestrator-side handler fires
// each spawn as a fresh Plan node (namespaced "{parent}/{spawnID}"),
// dispatches it through the regular scheduling loop, and blocks the
// parent runner until the child terminates.

// --- spawn-shape pipelines ---

type spawnedChildJob struct {
	sparkwing.Base
	tag string
	ran *atomic.Bool
}

func (j *spawnedChildJob) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	w.Step("run", func(ctx context.Context) error {
		if j.ran != nil {
			j.ran.Store(true)
		}
		sparkwing.Info(ctx, "spawned child %s ran", j.tag)
		return nil
	})
	return w
}

// spawnSingleParent declares one SpawnNode after a setup step. The
// parent's third step waits on the spawn and reads through
// SpawnHandle to confirm the suspended-runner round-trip.
type spawnSingleParent struct {
	sparkwing.Base
	childRan *atomic.Bool
}

func (j *spawnSingleParent) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	setup := w.Step("setup", func(ctx context.Context) error {
		sparkwing.Info(ctx, "parent setup")
		return nil
	})
	scan := w.SpawnNode("scan", &spawnedChildJob{tag: "scan", ran: j.childRan}).Needs(setup)
	w.Step("after", func(ctx context.Context) error {
		sparkwing.Info(ctx, "parent post-spawn")
		return nil
	}).Needs(scan)
	return w
}

type spawnSinglePipe struct {
	sparkwing.Base
	childRan *atomic.Bool
}

func (sp *spawnSinglePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", &spawnSingleParent{childRan: sp.childRan})
	return nil
}

// spawnFailingChild has a child whose Work errors; the spawn should
// surface the failure to the parent step, failing the parent.
type spawnFailingChild struct{ sparkwing.Base }

func (spawnFailingChild) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	w.Step("doomed", func(ctx context.Context) error { return errors.New("child boom") })
	return w
}

type spawnFailParent struct{ sparkwing.Base }

func (spawnFailParent) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	w.SpawnNode("doomed-child", spawnFailingChild{})
	return w
}

type spawnFailPipe struct{ sparkwing.Base }

func (spawnFailPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", spawnFailParent{})
	return nil
}

// spawnEachParent uses SpawnNodeForEach to fan out N children. Each
// child increments a shared counter; the test asserts the counter
// equals the slice length and that each child got a unique
// namespaced id in the plan.
type spawnEachParent struct {
	sparkwing.Base
	count *atomic.Int32
}

func (j *spawnEachParent) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	items := []string{"a", "b", "c"}
	w.SpawnNodeForEach(items, func(s string) (string, sparkwing.Workable) {
		tag := s
		return "shard-" + tag, sparkwing.JobFn(func(ctx context.Context) error {
			j.count.Add(1)
			sparkwing.Info(ctx, "shard %s ran", tag)
			return nil
		})
	})
	return w
}

type spawnEachPipe struct {
	sparkwing.Base
	count *atomic.Int32
}

func (sp *spawnEachPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", &spawnEachParent{count: sp.count})
	return nil
}

// --- shared test pipeline factory ---

var (
	spawnSingleChildRan atomic.Bool
	spawnEachCount      atomic.Int32
)

func init() {
	register("spawn-single", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &spawnSinglePipe{childRan: &spawnSingleChildRan}
	})
	register("spawn-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &spawnFailPipe{} })
	register("spawn-each", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &spawnEachPipe{count: &spawnEachCount}
	})
}

// --- tests ---

func TestSpawnDispatch_SingleSpawnRunsThroughHandler(t *testing.T) {
	spawnSingleChildRan.Store(false)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "spawn-single"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}
	if !spawnSingleChildRan.Load() {
		t.Fatal("spawned child did not execute")
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	want := map[string]bool{"parent": false, "parent/scan": false}
	for _, n := range nodes {
		if _, ok := want[n.NodeID]; ok {
			want[n.NodeID] = true
			if n.Outcome != string(sparkwing.Success) {
				t.Errorf("node %q outcome %q, want success", n.NodeID, n.Outcome)
			}
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("missing expected node %q in store; have %v", id, nodeIDs(nodes))
		}
	}
}

func TestSpawnDispatch_ChildFailureFailsParent(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "spawn-fail"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)

	parent, child := find(nodes, "parent"), find(nodes, "parent/doomed-child")
	if parent == nil || child == nil {
		t.Fatalf("missing nodes; have %v", nodeIDs(nodes))
	}
	if child.Outcome != string(sparkwing.Failed) || !strings.Contains(child.Error, "child boom") {
		t.Errorf("child outcome=%q error=%q, want failed/'child boom'", child.Outcome, child.Error)
	}
	if parent.Outcome != string(sparkwing.Failed) {
		t.Errorf("parent outcome=%q, want failed (child failure should cascade)", parent.Outcome)
	}
}

func TestSpawnDispatch_ForEachFansOut(t *testing.T) {
	spawnEachCount.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "spawn-each"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}
	if spawnEachCount.Load() != 3 {
		t.Fatalf("expected 3 child runs, got %d", spawnEachCount.Load())
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	wantIDs := map[string]bool{
		"parent":         false,
		"parent/shard-a": false,
		"parent/shard-b": false,
		"parent/shard-c": false,
	}
	for _, n := range nodes {
		if _, ok := wantIDs[n.NodeID]; ok {
			wantIDs[n.NodeID] = true
			if n.Outcome != string(sparkwing.Success) {
				t.Errorf("node %q outcome %q, want success", n.NodeID, n.Outcome)
			}
		}
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("missing expected node %q; have %v", id, nodeIDs(nodes))
		}
	}
}

// --- helpers ---

func find(nodes []*store.Node, id string) *store.Node {
	for _, n := range nodes {
		if n.NodeID == id {
			return n
		}
	}
	return nil
}

func nodeIDs(nodes []*store.Node) string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.NodeID)
	}
	return fmt.Sprintf("%v", ids)
}
