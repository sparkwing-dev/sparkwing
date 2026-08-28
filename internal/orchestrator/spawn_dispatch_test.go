package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type spawnedChildJob struct {
	sparkwing.Base
	tag string
	ran *atomic.Bool
}

func (j *spawnedChildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(ctx context.Context) error {
		if j.ran != nil {
			j.ran.Store(true)
		}
		sparkwing.Info(ctx, "spawned child %s ran", j.tag)
		return nil
	})
	return nil, nil
}

type spawnSingleParent struct {
	sparkwing.Base
	childRan *atomic.Bool
}

func (j *spawnSingleParent) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	setup := sparkwing.Step(w, "setup", func(ctx context.Context) error {
		sparkwing.Info(ctx, "parent setup")
		return nil
	})
	scan := sparkwing.JobSpawn(w, "scan", &spawnedChildJob{tag: "scan", ran: j.childRan}).Needs(setup)
	sparkwing.Step(w, "after", func(ctx context.Context) error {
		sparkwing.Info(ctx, "parent post-spawn")
		return nil
	}).Needs(scan)
	return nil, nil
}

type spawnSinglePipe struct {
	sparkwing.Base
	childRan *atomic.Bool
}

func (sp *spawnSinglePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", &spawnSingleParent{childRan: sp.childRan})
	return nil
}

type spawnFailingChild struct{ sparkwing.Base }

func (spawnFailingChild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "doomed", func(ctx context.Context) error { return errors.New("child boom") })
	return nil, nil
}

type spawnFailParent struct{ sparkwing.Base }

func (spawnFailParent) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.JobSpawn(w, "doomed-child", spawnFailingChild{})
	return nil, nil
}

type spawnFailPipe struct{ sparkwing.Base }

func (spawnFailPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", spawnFailParent{})
	return nil
}

type spawnProgressChild struct{ sparkwing.Base }

var (
	spawnProgressParentContext = make(chan context.Context, 1)
	spawnProgressChildContext  = make(chan context.Context, 1)
	spawnProgressChildRelease  = make(chan struct{})
	spawnProgressAfterContext  = make(chan context.Context, 1)
)

func (spawnProgressChild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "wait", func(ctx context.Context) error {
		select {
		case spawnProgressChildContext <- ctx:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-spawnProgressChildRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return nil, nil
}

type spawnProgressParent struct{ sparkwing.Base }

func (spawnProgressParent) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	before := sparkwing.Step(w, "before", func(ctx context.Context) error {
		select {
		case spawnProgressParentContext <- ctx:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	child := sparkwing.JobSpawn(w, "child", spawnProgressChild{}).Needs(before)
	sparkwing.Step(w, "after", func(ctx context.Context) error {
		select {
		case spawnProgressAfterContext <- ctx:
		case <-ctx.Done():
			return ctx.Err()
		}
		<-ctx.Done()
		return ctx.Err()
	}).Needs(child)
	return nil, nil
}

type spawnProgressPipe struct{ sparkwing.Base }

func (spawnProgressPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", spawnProgressParent{}).NoProgressTimeout(100 * time.Millisecond)
	return nil
}

type spawnEachParent struct {
	sparkwing.Base
	count *atomic.Int32
}

func (j *spawnEachParent) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	items := []string{"a", "b", "c"}
	sparkwing.JobSpawnEach(w, items, func(s string) (string, any) {
		tag := s
		return "shard-" + tag, func(ctx context.Context) error {
			j.count.Add(1)
			sparkwing.Info(ctx, "shard %s ran", tag)
			return nil
		}
	})
	return nil, nil
}

type spawnEachPipe struct {
	sparkwing.Base
	count *atomic.Int32
}

func (sp *spawnEachPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", &spawnEachParent{count: sp.count})
	return nil
}

var (
	spawnSingleChildRan atomic.Bool
	spawnEachCount      atomic.Int32
)

func init() {
	register("spawn-single", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &spawnSinglePipe{childRan: &spawnSingleChildRan}
	})
	register("spawn-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &spawnFailPipe{} })
	register("spawn-progress-timeout", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &spawnProgressPipe{} })
	register("spawn-each", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &spawnEachPipe{count: &spawnEachCount}
	})
}

func TestSpawnDispatch_NoProgressTimeoutPausesForChildAndResumesAfterward(t *testing.T) {
	spawnProgressParentContext = make(chan context.Context, 1)
	spawnProgressChildContext = make(chan context.Context, 1)
	spawnProgressChildRelease = make(chan struct{})
	spawnProgressAfterContext = make(chan context.Context, 1)
	var releaseChild sync.Once
	release := func() { releaseChild.Do(func() { close(spawnProgressChildRelease) }) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	type runResult struct {
		result *orchestrator.Result
		err    error
	}
	runDone := make(chan runResult, 1)
	runFinished := make(chan struct{})
	p := newPaths(t)
	go func() {
		defer close(runFinished)
		res, err := orchestrator.RunLocal(ctx, p, orchestrator.Options{Pipeline: "spawn-progress-timeout"})
		runDone <- runResult{result: res, err: err}
	}()
	t.Cleanup(func() {
		release()
		cancel()
		join := time.NewTimer(time.Second)
		defer join.Stop()
		select {
		case <-runFinished:
		case <-join.C:
			t.Error("spawn progress run did not stop")
		}
	})
	var parentCtx context.Context
	select {
	case parentCtx = <-spawnProgressParentContext:
	case <-ctx.Done():
		t.Fatal("parent did not start before spawning its child")
	}
	select {
	case <-spawnProgressChildContext:
	case <-ctx.Done():
		t.Fatal("spawned child did not start")
	}
	if !orchestrator.ProgressTimeoutPausedForTest(parentCtx) {
		t.Fatal("parent progress timeout was not paused for the spawned child")
	}
	if orchestrator.ExpireProgressTimeoutForTest(parentCtx) {
		t.Fatal("parent progress timeout fired while the spawned child was pending")
	}
	release()
	var afterCtx context.Context
	select {
	case afterCtx = <-spawnProgressAfterContext:
	case <-ctx.Done():
		t.Fatal("parent did not resume after the spawned child")
	}
	if !orchestrator.ExpireProgressTimeoutForTest(afterCtx) {
		t.Fatal("parent progress timeout did not fire after the spawned child completed")
	}
	var completed runResult
	select {
	case completed = <-runDone:
	case <-ctx.Done():
		t.Fatal("spawn progress run did not finish after forced timeout")
	}
	cancel()
	res, err := completed.result, completed.err
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want no-progress failure after child completed", res.Status)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	parent, child := find(nodes, "parent"), find(nodes, "parent/child")
	if parent == nil || parent.FailureReason != store.FailureNoProgressTimeout {
		t.Fatalf("parent = %+v, want failure reason %q", parent, store.FailureNoProgressTimeout)
	}
	if child == nil || child.Outcome != string(sparkwing.Success) {
		t.Fatalf("child = %+v, want success after outliving the parent's inactivity budget", child)
	}
}

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
	defer func() { _ = st.Close() }()
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
	defer func() { _ = st.Close() }()
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
	defer func() { _ = st.Close() }()
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
