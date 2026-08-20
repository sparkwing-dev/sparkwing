package orchestrator_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// sharedS3 is the mocked push target for both pipelines. Every time
// either pipeline's "push-s3" step fires, we observe the in-flight
// count. Peak must stay at 1.
var sharedS3 struct {
	inflight    atomic.Int32
	maxInflight atomic.Int32
	pushes      atomic.Int32
}

type sharedS3Gate struct {
	entered     chan struct{}
	release     chan struct{}
	finished    chan struct{}
	releaseOnce sync.Once
}

var activeSharedS3Gate atomic.Pointer[sharedS3Gate]

type sharedS3Result struct {
	name string
	res  *orchestrator.Result
	err  error
}

func resetSharedS3() {
	sharedS3.inflight.Store(0)
	sharedS3.maxInflight.Store(0)
	sharedS3.pushes.Store(0)
}

func s3Push() func(ctx context.Context) error {
	return func(ctx context.Context) error {
		cur := sharedS3.inflight.Add(1)
		defer sharedS3.inflight.Add(-1)
		for {
			peak := sharedS3.maxInflight.Load()
			if cur <= peak || sharedS3.maxInflight.CompareAndSwap(peak, cur) {
				break
			}
		}
		sharedS3.pushes.Add(1)
		gate := activeSharedS3Gate.Load()
		if gate == nil {
			return nil
		}
		select {
		case gate.entered <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-gate.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case gate.finished <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func unsharedStep() func(context.Context) error {
	return func(context.Context) error { return nil }
}

// publishReleasePipe: build -> push-s3 -> notify
type publishReleasePipe struct{ sparkwing.Base }

func (publishReleasePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build-artifact", unsharedStep())
	push := sparkwing.Job(plan, "push-s3", s3Push()).
		Needs(build).
		Concurrency(sparkwing.NewConcurrencyGroup("shared-s3-bucket", sparkwing.ConcurrencyLimit{Capacity: 1, OnLimit: sparkwing.Queue}))
	sparkwing.Job(plan, "notify-slack", unsharedStep()).Needs(push)
	return nil
}

// syncBackupPipe: snapshot -> push-s3 -> inventory
type syncBackupPipe struct{ sparkwing.Base }

func (syncBackupPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	snapshot := sparkwing.Job(plan, "snapshot-db", unsharedStep())
	push := sparkwing.Job(plan, "push-s3", s3Push()).
		Needs(snapshot).
		Concurrency(sparkwing.NewConcurrencyGroup("shared-s3-bucket", sparkwing.ConcurrencyLimit{Capacity: 1, OnLimit: sparkwing.Queue}))
	sparkwing.Job(plan, "update-inventory", unsharedStep()).Needs(push)
	return nil
}

func init() {
	register("publish-release", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &publishReleasePipe{} })
	register("sync-backup", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &syncBackupPipe{} })
}

// runWithSharedStore dispatches opts against a single shared Store.
// RunLocal opens its own *store.Store on every call, which races on
// schema migration when two callers fire concurrently against the
// same paths. Sharing one store mirrors how sparkwing dev actually
// works (one process, one store) and is the correct test topology.
func runWithSharedStore(ctx context.Context, t *testing.T, paths orchestrator.Paths, st *store.Store, opts orchestrator.Options) (*orchestrator.Result, error) {
	t.Helper()
	if err := paths.EnsureRoot(); err != nil {
		return nil, err
	}
	return orchestrator.Run(ctx, orchestrator.LocalBackends(paths, st, nil), opts)
}

func runSharedS3Burst(t *testing.T, p orchestrator.Paths, st *store.Store, names []string) []sharedS3Result {
	t.Helper()
	gate := &sharedS3Gate{
		entered:  make(chan struct{}, len(names)),
		release:  make(chan struct{}),
		finished: make(chan struct{}, len(names)),
	}
	if !activeSharedS3Gate.CompareAndSwap(nil, gate) {
		t.Fatal("shared S3 gate already installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	results := make(chan sharedS3Result, len(names))
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := runWithSharedStore(ctx, t, p, st, orchestrator.Options{Pipeline: name})
			results <- sharedS3Result{name: name, res: res, err: err}
		}()
	}
	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			gate.releaseOnce.Do(func() { close(gate.release) })
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-workersDone:
			case <-timer.C:
				t.Errorf("shared S3 workers did not stop within 1s")
			}
			activeSharedS3Gate.CompareAndSwap(gate, nil)
		})
	}
	t.Cleanup(cleanup)

	for admitted := 0; admitted < len(names); admitted++ {
		select {
		case <-gate.entered:
		case <-ctx.Done():
			t.Fatalf("shared S3 body %d of %d did not enter: %v", admitted+1, len(names), ctx.Err())
		}
		waitForCacheConcurrencyPopulation(t, ctx, p.StateDB(), "g:shared-s3-bucket", 1, len(names)-admitted-1)
		select {
		case <-gate.entered:
			t.Fatalf("multiple shared S3 bodies entered before release %d", admitted+1)
		default:
		}
		if peak := sharedS3.maxInflight.Load(); peak > 1 {
			t.Fatalf("push-s3 peak concurrency = %d before release %d, want 1", peak, admitted+1)
		}
		select {
		case gate.release <- struct{}{}:
		case <-ctx.Done():
			t.Fatalf("release shared S3 body %d: %v", admitted+1, ctx.Err())
		}
		select {
		case <-gate.finished:
		case <-ctx.Done():
			t.Fatalf("shared S3 body %d did not finish: %v", admitted+1, ctx.Err())
		}
	}
	select {
	case <-workersDone:
	case <-ctx.Done():
		t.Fatalf("shared S3 runs did not finish: %v", ctx.Err())
	}

	out := make([]sharedS3Result, 0, len(names))
	for range names {
		out = append(out, <-results)
	}
	cleanup()
	return out
}

func TestCache_TwoPipelinesShareKey_PushSerializes(t *testing.T) {
	resetSharedS3()
	p := newPaths(t)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	results := runSharedS3Burst(t, p, st, []string{"publish-release", "sync-backup"})

	var succeeded int
	for _, r := range results {
		if r.err != nil {
			t.Errorf("%s: %v", r.name, r.err)
			continue
		}
		if r.res.Status != "success" {
			t.Errorf("%s: status = %q", r.name, r.res.Status)
			continue
		}
		succeeded++
	}
	if succeeded != 2 {
		t.Fatalf("expected both pipelines to succeed, got %d", succeeded)
	}

	if peak := sharedS3.maxInflight.Load(); peak > 1 {
		t.Fatalf("push-s3 peak concurrency = %d, want 1 (shared cache key violated)", peak)
	}

	if pushes := sharedS3.pushes.Load(); pushes != 2 {
		t.Fatalf("expected 2 pushes total, got %d", pushes)
	}

	runs, _ := st.ListRuns(context.Background(), store.RunFilter{Limit: 5})
	pushNodes := 0
	for _, r := range runs {
		nodes, _ := st.ListNodes(context.Background(), r.ID)
		for _, n := range nodes {
			if n.NodeID == "push-s3" {
				if n.Outcome != string(sparkwing.Success) {
					t.Errorf("run %s: push-s3 outcome = %q, want success", r.ID, n.Outcome)
				}
				pushNodes++
			}
		}
	}
	if pushNodes != 2 {
		t.Fatalf("expected 2 push-s3 node rows (one per pipeline), got %d", pushNodes)
	}
}

// TestCache_TwoPipelinesShareKey_AcrossMultipleBursts verifies the
// coordination holds under sustained traffic: fire K pairs of the
// two pipelines over a loop. Peak inflight must stay at 1 over the
// whole duration.
func TestCache_TwoPipelinesShareKey_AcrossMultipleBursts(t *testing.T) {
	resetSharedS3()
	p := newPaths(t)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const iterations = 3
	pipelines := make([]string, 0, 2*iterations)
	for range iterations {
		for _, name := range []string{"publish-release", "sync-backup"} {
			pipelines = append(pipelines, name)
		}
	}
	results := runSharedS3Burst(t, p, st, pipelines)
	for i, result := range results {
		if result.err != nil {
			t.Errorf("run %d %s: %v", i, result.name, result.err)
			continue
		}
		if result.res.Status != "success" {
			t.Errorf("run %d %s: status=%q", i, result.name, result.res.Status)
		}
	}

	if peak := sharedS3.maxInflight.Load(); peak > 1 {
		t.Fatalf("push-s3 peak concurrency across bursts = %d, want 1", peak)
	}
	if pushes := sharedS3.pushes.Load(); pushes != int32(2*iterations) {
		t.Fatalf("expected %d pushes, got %d", 2*iterations, pushes)
	}
}

// Helper: print a compact state dump for ad-hoc debugging during
// test development. Unused by default; retained because the
// concurrency primitive's state is otherwise opaque from the test
// vantage point.
//
//lint:ignore U1000 ad-hoc debug helper retained for future test development
func debugConcurrencyState(t *testing.T, st *store.Store, key string) {
	state, err := st.GetConcurrencyState(context.Background(), key)
	if err != nil {
		t.Logf("state(%s): %v", key, err)
		return
	}
	t.Logf("state(%s): holders=%d waiters=%d", key, len(state.Holders), len(state.Waiters))
	for i, h := range state.Holders {
		t.Logf("  holder[%d] %s run=%s node=%s", i, h.HolderID, h.RunID, h.NodeID)
	}
	for i, w := range state.Waiters {
		t.Logf("  waiter[%d] run=%s node=%s policy=%s", i, w.RunID, w.NodeID, w.Policy)
	}
	_ = fmt.Sprintf
}
