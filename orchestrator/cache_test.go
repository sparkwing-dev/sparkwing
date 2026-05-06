package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// --- Pipelines used by the cache + hook tests ---

type cachedPipe struct{ sparkwing.Base }

var cachedInvocations atomic.Int32

func (cachedPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", &cachedBuildJob{})
	return nil
}

type cachedBuildOut struct {
	Tag string `json:"tag"`
}

type cachedBuildJob struct {
	sparkwing.Base
	sparkwing.Produces[cachedBuildOut]
}

func (j *cachedBuildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	out := sparkwing.Out(w, "run", j.run)
	return out.WorkStep, nil
}

func (cachedBuildJob) run(ctx context.Context) (cachedBuildOut, error) {
	cachedInvocations.Add(1)
	return cachedBuildOut{Tag: "v-cached"}, nil
}

// hooksPipe exercises BeforeRun + AfterRun.
type hooksPipe struct{ sparkwing.Base }

type hooksCounters struct {
	before int32
	after  int32
	ran    int32
}

var hooks hooksCounters

func resetHooksCounters() {
	atomic.StoreInt32(&hooks.before, 0)
	atomic.StoreInt32(&hooks.after, 0)
	atomic.StoreInt32(&hooks.ran, 0)
}

func (hooksPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "work", sparkwing.JobFn(func(ctx context.Context) error {
		atomic.AddInt32(&hooks.ran, 1)
		return nil
	})).
		BeforeRun(func(ctx context.Context) error {
			atomic.AddInt32(&hooks.before, 1)
			return nil
		}).
		AfterRun(func(ctx context.Context, err error) {
			atomic.AddInt32(&hooks.after, 1)
		})
	return nil
}

type beforeFails struct{ sparkwing.Base }

var beforeFailsRan atomic.Bool

func (beforeFails) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "never-runs", sparkwing.JobFn(func(ctx context.Context) error {
		beforeFailsRan.Store(true)
		return nil
	})).BeforeRun(func(ctx context.Context) error {
		return errors.New("refusing to run")
	})
	return nil
}

type afterFiresOnFailure struct{ sparkwing.Base }

var afterOnFailureErr atomic.Value // stores error

func (afterFiresOnFailure) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "boom", sparkwing.JobFn(func(ctx context.Context) error {
		return errors.New("job failed")
	})).AfterRun(func(ctx context.Context, err error) {
		afterOnFailureErr.Store(errorSentinel{err: err})
	})
	return nil
}

// hookOrdering: verifies hooks fire around Run in the expected
// sequence. Uses a recording slice protected by a mutex.
type hookOrderingPipe struct{ sparkwing.Base }

var hookOrderingLog struct {
	mu      atomic.Int32 // simple counter; sequence matters not concurrency
	entries [3]string
}

func recordHook(i int, label string) {
	pos := int(hookOrderingLog.mu.Add(1)) - 1
	if pos < len(hookOrderingLog.entries) {
		hookOrderingLog.entries[pos] = label
	}
}

func (hookOrderingPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "seq", sparkwing.JobFn(func(ctx context.Context) error {
		recordHook(1, "run")
		return nil
	})).
		BeforeRun(func(ctx context.Context) error {
			recordHook(0, "before")
			return nil
		}).
		AfterRun(func(ctx context.Context, err error) {
			recordHook(2, "after")
		})
	return nil
}

func init() {
	register("cache-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cachedPipe{} })
	register("hooks-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &hooksPipe{} })
	register("hooks-before-fails", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &beforeFails{} })
	register("hooks-after-on-failure", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &afterFiresOnFailure{} })
	register("hooks-ordering", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &hookOrderingPipe{} })
}

// --- Cache tests ---

func TestCacheKey_FirstRunRunsJob(t *testing.T) {
	cachedInvocations.Store(0)
	p := newPaths(t)
	// Plan-side: attach cache key to the build node.
	//
	// The cache test uses the registered pipeline which doesn't
	// attach a cache key by default. We pass the cache key via an
	// env-toggled variant: register a cache-enabled pipeline locally.
	enabledPipeline := func() sparkwing.Pipeline[sparkwing.NoInputs] {
		pipe := &cachedPipe{}
		return wrapWithCacheKey(pipe)
	}
	sparkwing.Register[sparkwing.NoInputs]("cache-keyed", enabledPipeline)
	defer func() {
		// best-effort cleanup; Register panics on duplicates so we
		// can't simply re-use. Tests run once per name.
	}()

	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-keyed"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q", res.Status)
	}
	if got := cachedInvocations.Load(); got != 1 {
		t.Fatalf("first run should invoke Run once, got %d", got)
	}

	// Verify an entry landed in the cache table.
	st, _ := store.Open(p.StateDB())
	defer st.Close()

	// We don't know the exact key string, but we know there must be
	// exactly one cache row with a matching output payload.
	rows := countCacheRows(t, st)
	if rows != 1 {
		t.Fatalf("expected 1 cache row, got %d", rows)
	}
}

func TestCacheKey_SecondRunReplaysOutput(t *testing.T) {
	cachedInvocations.Store(0)
	p := newPaths(t)

	// Register a fresh pipeline for isolation since this test
	// expects two sequential runs on a fresh cache.
	sparkwing.Register[sparkwing.NoInputs]("cache-replay", func() sparkwing.Pipeline[sparkwing.NoInputs] { return wrapWithCacheKey(&cachedPipe{}) })

	// First run -- populates the cache.
	res1, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-replay"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run -- should hit the cache and not invoke Run again.
	res2, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-replay"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if got := cachedInvocations.Load(); got != 1 {
		t.Fatalf("Run should be invoked once total across two runs, got %d", got)
	}

	// Second run's "build" node must be Cached.
	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res2.RunID)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if nodes[0].Outcome != string(sparkwing.Cached) {
		t.Fatalf("node outcome = %q, want cached", nodes[0].Outcome)
	}

	// Output should roundtrip through the cache.
	var out cachedBuildOut
	if err := json.Unmarshal(nodes[0].Output, &out); err != nil {
		t.Fatalf("unmarshal cached output: %v", err)
	}
	if out.Tag != "v-cached" {
		t.Fatalf("cached output = %+v", out)
	}

	_ = res1
	_ = time.Millisecond // avoid unused import if we trim later
}

func TestCacheKey_EmptyKeyDisablesCaching(t *testing.T) {
	cachedInvocations.Store(0)
	p := newPaths(t)

	// Register a pipeline whose cache key function returns the empty
	// string -- should behave like no cache key at all.
	sparkwing.Register[sparkwing.NoInputs]("cache-empty-key", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return wrapWithSpecificKey(&cachedPipe{}, func(ctx context.Context) sparkwing.CacheKey {
			return ""
		})
	})

	for i := range 2 {
		_, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-empty-key"})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if got := cachedInvocations.Load(); got != 2 {
		t.Fatalf("empty key should disable caching; got %d invocations, want 2", got)
	}
}

// --- Hook tests ---

func TestHooks_BeforeAndAfterFire(t *testing.T) {
	resetHooksCounters()
	p := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "hooks-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if b := atomic.LoadInt32(&hooks.before); b != 1 {
		t.Fatalf("before hooks fired %d times, want 1", b)
	}
	if r := atomic.LoadInt32(&hooks.ran); r != 1 {
		t.Fatalf("work ran %d times, want 1", r)
	}
	if a := atomic.LoadInt32(&hooks.after); a != 1 {
		t.Fatalf("after hooks fired %d times, want 1", a)
	}
}

func TestHooks_BeforeRunFailureStopsJob(t *testing.T) {
	beforeFailsRan.Store(false)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "hooks-before-fails"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if beforeFailsRan.Load() {
		t.Fatal("Run should not execute when BeforeRun fails")
	}
}

func TestHooks_AfterRunFiresOnFailure(t *testing.T) {
	afterOnFailureErr.Store(errorSentinel{})
	p := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "hooks-after-on-failure"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	v := afterOnFailureErr.Load()
	recorded, ok := v.(errorSentinel)
	if !ok {
		t.Fatalf("AfterRun did not fire: got %+v", v)
	}
	if recorded.err == nil {
		t.Fatal("AfterRun did not receive the run error")
	}
	if !strings.Contains(recorded.err.Error(), "job failed") {
		t.Fatalf("unexpected err forwarded: %v", recorded.err)
	}
}

func TestHooks_Ordering(t *testing.T) {
	hookOrderingLog.mu.Store(0)
	hookOrderingLog.entries = [3]string{}
	p := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "hooks-ordering"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := hookOrderingLog.entries
	want := [3]string{"before", "run", "after"}
	if got != want {
		t.Fatalf("ordering = %v, want %v", got, want)
	}
}

// --- test helpers ---

// wrapWithCacheKey returns a Pipeline whose plan attaches a constant
// CacheKey to the first node. Used by the cache tests above since
// cachedPipe itself doesn't declare a key by default.
func wrapWithCacheKey(p *cachedPipe) sparkwing.Pipeline[sparkwing.NoInputs] {
	return wrapWithSpecificKey(p, func(ctx context.Context) sparkwing.CacheKey {
		return sparkwing.Key("cache-test", "static")
	})
}

func wrapWithSpecificKey(p *cachedPipe, keyFn sparkwing.CacheKeyFn) sparkwing.Pipeline[sparkwing.NoInputs] {
	return &planWrapper{inner: p, keyFn: keyFn}
}

type planWrapper struct {
	sparkwing.Base
	inner *cachedPipe
	keyFn sparkwing.CacheKeyFn
}

func (w *planWrapper) Plan(ctx context.Context, plan *sparkwing.Plan, in sparkwing.NoInputs, rc sparkwing.RunContext) error {
	if err := w.inner.Plan(ctx, plan, in, rc); err != nil {
		return err
	}
	if node := plan.Node("build"); node != nil {
		node.Cache(sparkwing.CacheOptions{
			Key:      "cache-test-build",
			OnLimit:  sparkwing.Coalesce,
			CacheKey: w.keyFn,
		})
	}
	return nil
}

// errorSentinel lets us store an error in an atomic.Value (which
// requires a consistent concrete type across all stores).
type errorSentinel struct{ err error }

func countCacheRows(t *testing.T, st *store.Store) int {
	t.Helper()
	n, err := st.CountConcurrencyCache(context.Background())
	if err != nil {
		t.Fatalf("CountConcurrencyCache: %v", err)
	}
	return n
}
