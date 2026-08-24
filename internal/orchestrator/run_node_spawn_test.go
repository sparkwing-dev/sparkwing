package orchestrator_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type podSpawnScanOut struct {
	Findings int `json:"findings"`
}

type podSpawnChild struct {
	sparkwing.Base
	sparkwing.Produces[podSpawnScanOut]
}

func (podSpawnChild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "scan", func(ctx context.Context) (podSpawnScanOut, error) {
		podSpawnSeen.record("child", sparkwing.NodeFromContext(ctx))
		return podSpawnScanOut{Findings: 7}, nil
	}), nil
}

type podSpawnFailingChild struct{ sparkwing.Base }

func (podSpawnFailingChild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "boom", func(ctx context.Context) error {
		return errPodSpawnChild
	})
	return nil, nil
}

type podSpawnParent struct{ sparkwing.Base }

func (podSpawnParent) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	setup := sparkwing.Step(w, "setup", func(ctx context.Context) error {
		podSpawnSeen.record("parent", sparkwing.NodeFromContext(ctx))
		return nil
	})
	scan := sparkwing.JobSpawn(w, "scan", podSpawnChild{}).Needs(setup)
	sparkwing.Step(w, "after", func(ctx context.Context) error {
		podSpawnSeen.record("after", sparkwing.NodeFromContext(ctx))
		return nil
	}).Needs(scan)
	return nil, nil
}

type podSpawnFailParent struct{ sparkwing.Base }

func (podSpawnFailParent) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.JobSpawn(w, "doomed", podSpawnFailingChild{})
	return nil, nil
}

type podSpawnPipe struct{ sparkwing.Base }

func (podSpawnPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", podSpawnParent{})
	return nil
}

type podSpawnFailPipe struct{ sparkwing.Base }

func (podSpawnFailPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", podSpawnFailParent{})
	return nil
}

var errPodSpawnChild = &podSpawnChildError{}

type podSpawnChildError struct{}

func (*podSpawnChildError) Error() string { return "child scan exploded" }

// podSpawnGatedChild declares a runner label no ordinary box carries,
// so both dispatch paths have to refuse it.
type podSpawnGatedChild struct{ sparkwing.Base }

func (podSpawnGatedChild) WhenRunner() []string { return []string{"gpu"} }

func (podSpawnGatedChild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "burn", func(ctx context.Context) error {
		podSpawnSeen.record("gated", sparkwing.NodeFromContext(ctx))
		return nil
	})
	return nil, nil
}

type podSpawnGatedParent struct{ sparkwing.Base }

func (podSpawnGatedParent) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	gated := sparkwing.JobSpawn(w, "gated", podSpawnGatedChild{})
	sparkwing.Step(w, "after", func(ctx context.Context) error {
		podSpawnSeen.record("after-gated", sparkwing.NodeFromContext(ctx))
		return nil
	}).Needs(gated)
	return nil, nil
}

type podSpawnGatedPipe struct{ sparkwing.Base }

func (podSpawnGatedPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", podSpawnGatedParent{})
	return nil
}

type podSpawnEachParent struct{ sparkwing.Base }

func (podSpawnEachParent) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.JobSpawnEach(w, []string{"a", "b", "c"}, func(s string) (string, any) {
		tag := s
		return "shard-" + tag, func(ctx context.Context) error {
			podSpawnEachRan.record("shard-"+tag, sparkwing.NodeFromContext(ctx))
			return nil
		}
	})
	return nil, nil
}

type podSpawnEachPipe struct{ sparkwing.Base }

func (podSpawnEachPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", podSpawnEachParent{})
	return nil
}

var podSpawnEachRan = &podSpawnRecorder{m: map[string]string{}}

// podSpawnProgress* mirror spawn_dispatch_test.go's no-progress
// fixture on the node path: the parent hands out its own context and
// its child's, and the child blocks until released.
var (
	podSpawnProgressParentCtx = make(chan context.Context, 1)
	podSpawnProgressChildCtx  = make(chan context.Context, 1)
	podSpawnProgressRelease   = make(chan struct{})
)

type podSpawnProgressChild struct{ sparkwing.Base }

func (podSpawnProgressChild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "wait", func(ctx context.Context) error {
		select {
		case podSpawnProgressChildCtx <- ctx:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-podSpawnProgressRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return nil, nil
}

type podSpawnProgressParent struct{ sparkwing.Base }

func (podSpawnProgressParent) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	before := sparkwing.Step(w, "before", func(ctx context.Context) error {
		select {
		case podSpawnProgressParentCtx <- ctx:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	sparkwing.JobSpawn(w, "child", podSpawnProgressChild{}).Needs(before)
	return nil, nil
}

type podSpawnProgressPipe struct{ sparkwing.Base }

func (podSpawnProgressPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "parent", podSpawnProgressParent{}).NoProgressTimeout(100 * time.Millisecond)
	return nil
}

// podSpawnRecorder records which node id each body observed, so the
// tests can assert the child ran under its own namespaced identity
// rather than borrowing its parent's.
type podSpawnRecorder struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *podSpawnRecorder) record(key, nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = nodeID
}

func (s *podSpawnRecorder) get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[key]
}

func (s *podSpawnRecorder) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = map[string]string{}
}

var podSpawnSeen = &podSpawnRecorder{m: map[string]string{}}

func registerPodSpawnPipes() {
	register("pod-spawn", func() sparkwing.Pipeline[sparkwing.NoInputs] { return podSpawnPipe{} })
	register("pod-spawn-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return podSpawnFailPipe{} })
	register("pod-spawn-gated", func() sparkwing.Pipeline[sparkwing.NoInputs] { return podSpawnGatedPipe{} })
	register("pod-spawn-each", func() sparkwing.Pipeline[sparkwing.NoInputs] { return podSpawnEachPipe{} })
	register("pod-spawn-progress", func() sparkwing.Pipeline[sparkwing.NoInputs] { return podSpawnProgressPipe{} })
}

// podSpawnFixture stands a controller up over a fresh store and seeds
// a running one-node run, the shape a pod picks up.
func podSpawnFixture(t *testing.T, pipeline, runID, nodeID string) (*store.Store, string) {
	t.Helper()
	registerPodSpawnPipes()
	podSpawnSeen.reset()
	isolateCheckout(t)
	isolateProfiles(t)

	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	p := orchestrator.PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).Handler())
	t.Cleanup(srv.Close)

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  pipeline,
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	return st, srv.URL
}

// A node executing outside the dispatcher's process -- a pod, or since
// process-per-node any local node -- has no dispatcher to route
// SpawnNode through. Until RunNodeOnce installed a handler of its own,
// every such node whose Work declared a spawn failed at the first
// spawn with "no SpawnHandler is installed in ctx": the feature worked
// only where the parent happened to share the dispatcher's memory.
//
// The spawn_dispatched payload is asserted quoted because the store
// renders a non-JSON event payload as a JSON string on read.
func TestRunNodeOnce_SpawnRunsInsideTheNodeProcess(t *testing.T) {
	st, controllerURL := podSpawnFixture(t, "pod-spawn", "run-pod-spawn", "parent")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	res, err := orchestrator.RunNodeOnce(ctx, controllerURL, "", "run-pod-spawn", "parent",
		"pod:run-pod-spawn:parent", "", &captureLogger{}, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
	}

	childNode := podSpawnSeen.get("child")
	afterRan := podSpawnSeen.get("after")
	if childNode != "parent/scan" {
		t.Errorf("child body saw node %q, want parent/scan", childNode)
	}
	if afterRan != "parent" {
		t.Errorf("the parent's post-spawn step did not run (node=%q)", afterRan)
	}

	child, err := st.GetNode(ctx, "run-pod-spawn", "parent/scan")
	if err != nil || child == nil {
		t.Fatalf("spawn child row missing: %v", err)
	}
	if child.Outcome != string(sparkwing.Success) {
		t.Errorf("child outcome = %q (err=%q), want success", child.Outcome, child.Error)
	}
	if got := string(child.Output); got != `{"findings":7}` {
		t.Errorf("child output = %s, want {\"findings\":7} -- a Ref reader sees this row", got)
	}

	events, err := st.ListEventsAfter(ctx, "run-pod-spawn", 0, 500)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var dispatched bool
	for _, ev := range events {
		if ev.Kind == "spawn_dispatched" && ev.NodeID == "parent" && string(ev.Payload) == `"parent/scan"` {
			dispatched = true
		}
	}
	if !dispatched {
		for _, ev := range events {
			t.Logf("event %s/%s %q", ev.NodeID, ev.Kind, string(ev.Payload))
		}
		t.Error("no spawn_dispatched event on the parent naming parent/scan; the child has no recorded lineage")
	}
}

// A spawned child that fails has to fail its parent with the child's
// own message, the same as on the dispatcher's path -- otherwise a pod
// reports a green parent over failed sub-work.
func TestRunNodeOnce_SpawnChildFailureFailsTheParent(t *testing.T) {
	st, controllerURL := podSpawnFixture(t, "pod-spawn-fail", "run-pod-spawn-fail", "parent")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	res, err := orchestrator.RunNodeOnce(ctx, controllerURL, "", "run-pod-spawn-fail", "parent",
		"pod:run-pod-spawn-fail:parent", "", &captureLogger{}, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %q, want failed", res.Outcome)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "child scan exploded") {
		t.Errorf("parent error = %v, want the child's own message", res.Err)
	}

	child, err := st.GetNode(ctx, "run-pod-spawn-fail", "parent/doomed")
	if err != nil || child == nil {
		t.Fatalf("spawn child row missing: %v", err)
	}
	if child.Outcome != string(sparkwing.Failed) || !strings.Contains(child.Error, "child scan exploded") {
		t.Errorf("child row = outcome %q error %q, want failed with the child's message",
			child.Outcome, child.Error)
	}
	parent, err := st.GetNode(ctx, "run-pod-spawn-fail", "parent")
	if err != nil || parent == nil {
		t.Fatalf("parent row missing: %v", err)
	}
	if parent.Outcome != string(sparkwing.Failed) {
		t.Errorf("parent outcome = %q, want failed", parent.Outcome)
	}
}

// WhenRunner is enforced in the dispatch loop, above the runner, so a
// child spawned inside a node process reached no gate: it ran on
// whatever box its parent landed on and recorded success. Since
// process-per-node the dispatcher no longer executes node bodies at
// all, which makes this path the only one a `sparkwing run` uses.
//
// The skip is asserted against the dispatcher's own row for the same
// pipeline, so the two paths cannot drift in outcome or wording.
func TestRunNodeOnce_SpawnHonorsWhenRunnerLikeTheDispatcher(t *testing.T) {
	st, controllerURL := podSpawnFixture(t, "pod-spawn-gated", "run-pod-spawn-gated", "parent")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	res, err := orchestrator.RunNodeOnce(ctx, controllerURL, "", "run-pod-spawn-gated", "parent",
		"pod:run-pod-spawn-gated:parent", "", &captureLogger{}, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Success {
		t.Fatalf("parent outcome = %q (err=%v); a skipped child must not fail its parent",
			res.Outcome, res.Err)
	}
	if ran := podSpawnSeen.get("gated"); ran != "" {
		t.Errorf("the gpu-only child ran (node %q) on a runner advertising no such label", ran)
	}
	if podSpawnSeen.get("after-gated") != "parent" {
		t.Error("the parent's post-spawn step did not run after the child was skipped")
	}

	nodeSkip, err := st.GetNode(ctx, "run-pod-spawn-gated", "parent/gated")
	if err != nil || nodeSkip == nil {
		t.Fatalf("skipped child row missing: %v", err)
	}
	if nodeSkip.Outcome != string(sparkwing.Skipped) {
		t.Fatalf("child outcome = %q, want skipped", nodeSkip.Outcome)
	}
	events, err := st.ListEventsAfter(ctx, "run-pod-spawn-gated", 0, 500)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var skipEvent bool
	for _, ev := range events {
		if ev.Kind == "node_skipped" && ev.NodeID == "parent/gated" {
			skipEvent = true
		}
	}
	if !skipEvent {
		t.Error("no node_skipped event for the gated child")
	}

	// safety: the dispatcher path still executes node bodies in a test
	// binary, so its row for the same pipeline is the reference the node
	// path has to match.
	p := newPaths(t)
	ref, err := orchestrator.RunLocal(ctx, p, orchestrator.Options{Pipeline: "pod-spawn-gated"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	refStore, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open dispatcher store: %v", err)
	}
	defer func() { _ = refStore.Close() }()
	refChild, err := refStore.GetNode(ctx, ref.RunID, "parent/gated")
	if err != nil || refChild == nil {
		t.Fatalf("dispatcher child row missing: %v", err)
	}
	if refChild.Outcome != nodeSkip.Outcome || refChild.Error != nodeSkip.Error {
		t.Errorf("node path wrote outcome=%q error=%q; dispatcher wrote outcome=%q error=%q",
			nodeSkip.Outcome, nodeSkip.Error, refChild.Outcome, refChild.Error)
	}
}

// The other direction: on a runner that does advertise the label, the
// same child runs. The labels come from the RunnerInfo the run stamps
// on the process, which is what a spawned node and a pod each carry.
func TestRunNodeOnce_SpawnRunsWhenTheRunnerAdvertisesTheLabel(t *testing.T) {
	st, controllerURL := podSpawnFixture(t, "pod-spawn-gated", "run-pod-spawn-gpu", "parent")
	t.Setenv("SPARKWING_RUNNER_TYPE", "local")
	t.Setenv("SPARKWING_RUNNER_LABELS", "local,gpu")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	res, err := orchestrator.RunNodeOnce(ctx, controllerURL, "", "run-pod-spawn-gpu", "parent",
		"pod:run-pod-spawn-gpu:parent", "", &captureLogger{}, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
	}
	if podSpawnSeen.get("gated") != "parent/gated" {
		t.Error("the gpu child did not run on a runner advertising gpu")
	}
	child, err := st.GetNode(ctx, "run-pod-spawn-gpu", "parent/gated")
	if err != nil || child == nil {
		t.Fatalf("child row missing: %v", err)
	}
	if child.Outcome != string(sparkwing.Success) {
		t.Errorf("child outcome = %q, want success", child.Outcome)
	}
}

// The fan-out guarantee spawn_dispatch_test.go proves on the
// dispatcher path, ported to the path a `sparkwing run` now takes.
func TestRunNodeOnce_SpawnEachFansOut(t *testing.T) {
	st, controllerURL := podSpawnFixture(t, "pod-spawn-each", "run-pod-spawn-each", "parent")
	podSpawnEachRan.reset()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	res, err := orchestrator.RunNodeOnce(ctx, controllerURL, "", "run-pod-spawn-each", "parent",
		"pod:run-pod-spawn-each:parent", "", &captureLogger{}, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
	}
	for _, shard := range []string{"shard-a", "shard-b", "shard-c"} {
		if got := podSpawnEachRan.get(shard); got != "parent/"+shard {
			t.Errorf("%s ran as node %q, want parent/%s", shard, got, shard)
		}
		row, err := st.GetNode(ctx, "run-pod-spawn-each", "parent/"+shard)
		if err != nil || row == nil {
			t.Fatalf("row for parent/%s missing: %v", shard, err)
		}
		if row.Outcome != string(sparkwing.Success) {
			t.Errorf("parent/%s outcome = %q, want success", shard, row.Outcome)
		}
	}
}

// A parent blocked on its child is not a parent making no progress.
// The dispatcher's handler pauses the no-progress budget across the
// spawn; this asserts the node path does too, which is where the
// guarantee now has to hold.
func TestRunNodeOnce_SpawnPausesTheParentsNoProgressBudget(t *testing.T) {
	_, controllerURL := podSpawnFixture(t, "pod-spawn-progress", "run-pod-spawn-progress", "parent")
	podSpawnProgressParentCtx = make(chan context.Context, 1)
	podSpawnProgressChildCtx = make(chan context.Context, 1)
	podSpawnProgressRelease = make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(podSpawnProgressRelease) }) }
	t.Cleanup(release)

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan runner.Result, 1)
	go func() {
		res, err := orchestrator.RunNodeOnce(ctx, controllerURL, "", "run-pod-spawn-progress", "parent",
			"pod:run-pod-spawn-progress:parent", "", &captureLogger{}, quiet, nil)
		if err != nil {
			t.Errorf("RunNodeOnce: %v", err)
		}
		done <- res
	}()

	var parentCtx context.Context
	select {
	case parentCtx = <-podSpawnProgressParentCtx:
	case <-ctx.Done():
		t.Fatal("parent never started")
	}
	select {
	case <-podSpawnProgressChildCtx:
	case <-ctx.Done():
		t.Fatal("spawned child never started")
	}
	if !orchestrator.ProgressTimeoutPausedForTest(parentCtx) {
		t.Fatal("the parent's no-progress budget kept running while it waited on its child")
	}
	if orchestrator.ExpireProgressTimeoutForTest(parentCtx) {
		t.Fatal("the parent's no-progress timeout fired while its child was pending")
	}
	release()

	select {
	case res := <-done:
		if res.Outcome != sparkwing.Success {
			t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
		}
	case <-ctx.Done():
		t.Fatal("run did not finish after the child was released")
	}
}
