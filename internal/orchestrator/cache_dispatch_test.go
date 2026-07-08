package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var cacheCounter struct {
	inflight atomic.Int32
	max      atomic.Int32
}

func cacheStep(hold time.Duration) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		cur := cacheCounter.inflight.Add(1)
		defer cacheCounter.inflight.Add(-1)
		for {
			peak := cacheCounter.max.Load()
			if cur <= peak || cacheCounter.max.CompareAndSwap(peak, cur) {
				break
			}
		}
		time.Sleep(hold)
		return nil
	}
}

type cacheQueuePipe struct{ sparkwing.Base }

func (cacheQueuePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-queue-key", sparkwing.ConcurrencyLimit{Capacity: 1})
	sparkwing.Job(plan, "a", cacheStep(120*time.Millisecond)).Concurrency(g)
	sparkwing.Job(plan, "b", cacheStep(120*time.Millisecond)).Concurrency(g)
	return nil
}

type cacheSkipLeaderPipe struct{ sparkwing.Base }

func (cacheSkipLeaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-skip-key", sparkwing.ConcurrencyLimit{Capacity: 1})
	sparkwing.Job(plan, "leader", held(cacheCounterBump)).Concurrency(g)
	return nil
}

type cacheSkipFollowerPipe struct{ sparkwing.Base }

func (cacheSkipFollowerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-skip-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Skip,
	})
	sparkwing.Job(plan, "follower", cacheStep(50*time.Millisecond)).Concurrency(g)
	return nil
}

type cacheFailLeaderPipe struct{ sparkwing.Base }

func (cacheFailLeaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-fail-key", sparkwing.ConcurrencyLimit{Capacity: 1})
	sparkwing.Job(plan, "leader", held(cacheCounterBump)).Concurrency(g)
	return nil
}

type cacheFailFollowerPipe struct{ sparkwing.Base }

func (cacheFailFollowerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-fail-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Fail,
	})
	sparkwing.Job(plan, "follower", cacheStep(50*time.Millisecond)).Concurrency(g)
	return nil
}

type cacheCancelOthersLeaderPipe struct{ sparkwing.Base }

func (cacheCancelOthersLeaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-cancel-others-key", sparkwing.ConcurrencyLimit{Capacity: 1})
	sparkwing.Job(plan, "leader", func(ctx context.Context) error {
		select {
		case <-time.After(5 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}).Concurrency(g)
	return nil
}

type cacheCancelOthersFollowerPipe struct{ sparkwing.Base }

func (cacheCancelOthersFollowerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-cancel-others-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.CancelOthers,
	})
	sparkwing.Job(plan, "follower", cacheStep(50*time.Millisecond)).Concurrency(g)
	return nil
}

// cacheKeyedPipe exercises Cache() memoization across two sequential
// runs. First run misses and writes a cache entry; second run hits and
// replays the output without invoking the job body. Caching is keyed on
// content alone -- no concurrency group involved.
type cacheKeyedPipe struct{ sparkwing.Base }

func (cacheKeyedPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", func(ctx context.Context) error {
		cacheCounter.inflight.Add(1)
		return nil
	}).Cache(
		func(ctx context.Context) sparkwing.CacheKey { return "v-pinned" },
		sparkwing.TTL(time.Hour))
	return nil
}

// cacheDriftPipe declares the same group name with different capacities
// across two runs so the second run's acquire records a capacity drift.
type cacheDriftPipeA struct{ sparkwing.Base }

func (cacheDriftPipeA) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-drift-key", sparkwing.ConcurrencyLimit{Capacity: 1})
	sparkwing.Job(plan, "a", cacheStep(50*time.Millisecond)).Concurrency(g)
	return nil
}

type cacheDriftPipeB struct{ sparkwing.Base }

func (cacheDriftPipeB) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-drift-key", sparkwing.ConcurrencyLimit{Capacity: 3})
	sparkwing.Job(plan, "a", cacheStep(50*time.Millisecond)).Concurrency(g)
	return nil
}

// planLevelQueuePipe: single-node plan gated by Plan.Concurrency at
// capacity 1. Running two concurrently MUST serialize -- peak
// concurrency across both runs' nodes should stay at 1.
type planLevelQueuePipe struct{ sparkwing.Base }

func (planLevelQueuePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-key", sparkwing.ConcurrencyLimit{Capacity: 1}))
	sparkwing.Job(plan, "work", cacheStep(200*time.Millisecond))
	return nil
}

// planLevelSkipFollowerPipe: Skip-policy plan-level arrival that should
// no-op when a plan-level leader is already holding the key.
type planLevelSkipFollowerPipe struct{ sparkwing.Base }

func (planLevelSkipFollowerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-skip-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Skip,
	}))
	sparkwing.Job(plan, "work", cacheStep(100*time.Millisecond))
	return nil
}

type planLevelSkipLeaderPipe struct{ sparkwing.Base }

func (planLevelSkipLeaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-skip-key", sparkwing.ConcurrencyLimit{Capacity: 1}))
	sparkwing.Job(plan, "work", cacheStep(500*time.Millisecond))
	return nil
}

type planLevelInheritedChildPipe struct{ sparkwing.Base }

func (planLevelInheritedChildPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-inherited-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	}))
	sparkwing.Job(plan, "work", cacheStep(10*time.Millisecond))
	return nil
}

type planLevelInheritedSpawnerPipe struct{ sparkwing.Base }

func (planLevelInheritedSpawnerPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-inherited-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	}))
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](
			ctx, "plan-level-inherited-child", "work",
			sparkwing.WithFreshTimeout(150*time.Millisecond))
		return err
	})
	return nil
}

type planLevelInheritedMiddlePipe struct{ sparkwing.Base }

func (planLevelInheritedMiddlePipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](
			ctx, "plan-level-inherited-child", "work",
			sparkwing.WithFreshTimeout(150*time.Millisecond))
		return err
	})
	return nil
}

func init() {
	register("cache-queue-serialize", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheQueuePipe{} })
	register("cache-skip-leader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheSkipLeaderPipe{} })
	register("cache-skip-follower", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheSkipFollowerPipe{} })
	register("cache-fail-leader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheFailLeaderPipe{} })
	register("cache-fail-follower", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheFailFollowerPipe{} })
	register("cache-cancel-others-leader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheCancelOthersLeaderPipe{} })
	register("cache-cancel-others-follower", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheCancelOthersFollowerPipe{} })
	register("cache-memoize", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheKeyedPipe{} })
	register("cache-drift-a", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheDriftPipeA{} })
	register("cache-drift-b", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheDriftPipeB{} })
	register("plan-level-queue", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &planLevelQueuePipe{} })
	register("plan-level-skip-leader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &planLevelSkipLeaderPipe{} })
	register("plan-level-skip-follower", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &planLevelSkipFollowerPipe{} })
	register("plan-level-inherited-child", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelInheritedChildPipe{}
	})
	register("plan-level-inherited-spawner", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelInheritedSpawnerPipe{}
	})
	register("plan-level-inherited-middle", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelInheritedMiddlePipe{}
	})
}

func resetCacheCounter() {
	cacheCounter.inflight.Store(0)
	cacheCounter.max.Store(0)
	resetLeaderBarrier()
}

// cacheCounterBump records one in-flight body against the peak gauge and
// returns the matching decrement, for use as held()'s onStart.
func cacheCounterBump() func() {
	cur := cacheCounter.inflight.Add(1)
	for {
		peak := cacheCounter.max.Load()
		if cur <= peak || cacheCounter.max.CompareAndSwap(peak, cur) {
			break
		}
	}
	return func() { cacheCounter.inflight.Add(-1) }
}

func TestConcurrency_QueueSerializesConcurrentHolders(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-queue-serialize"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q err=%v", res.Status, res.Error)
	}
	if peak := cacheCounter.max.Load(); peak > 1 {
		t.Fatalf("Concurrency(Queue) peak concurrency = %d, want 1", peak)
	}
}

func TestConcurrency_QueueSerializesAcrossRuns(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-queue-serialize"})
		}()
	}
	wg.Wait()

	if peak := cacheCounter.max.Load(); peak > 1 {
		t.Fatalf("Concurrency(Queue) cross-run peak concurrency = %d, want 1", peak)
	}
}

func TestConcurrency_SkipResolvesAsSkippedConcurrent(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)

	leaderDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-skip-leader"})
		leaderDone <- res
	}()
	waitForLeaderHolding(t)

	followerRes, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-skip-follower"})
	if err != nil {
		t.Fatalf("follower run: %v", err)
	}
	if followerRes.Status != "success" {
		t.Fatalf("follower status = %q, want success (skipped-concurrent counts as OK)", followerRes.Status)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	fnodes, _ := st.ListNodes(context.Background(), followerRes.RunID)
	if len(fnodes) != 1 {
		t.Fatalf("follower: expected 1 node, got %d", len(fnodes))
	}
	if fnodes[0].Outcome != string(sparkwing.SkippedConcurrent) {
		t.Fatalf("follower outcome = %q, want skipped-concurrent", fnodes[0].Outcome)
	}

	leaderRelease.Store(true)
	leaderRes := <-leaderDone
	if leaderRes.Status != "success" {
		t.Fatalf("leader status = %q, want success", leaderRes.Status)
	}

	if peak := cacheCounter.max.Load(); peak > 1 {
		t.Fatalf("peak concurrency = %d, want <= 1", peak)
	}
}

func TestConcurrency_FailResolvesFollowerAsFailed(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)

	leaderDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-fail-leader"})
		leaderDone <- res
	}()
	waitForLeaderHolding(t)

	followerRes, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-fail-follower"})
	if followerRes.Status != "failed" {
		t.Fatalf("follower status = %q, want failed (OnLimit:Fail under held slot)", followerRes.Status)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), followerRes.RunID)
	if len(nodes) != 1 {
		t.Fatalf("follower run: expected 1 node, got %d", len(nodes))
	}
	if nodes[0].Outcome != string(sparkwing.Failed) {
		t.Fatalf("follower node outcome = %q, want failed", nodes[0].Outcome)
	}
	if !strings.Contains(nodes[0].Error, "OnLimit:Fail") {
		t.Fatalf("follower error = %q, want a message mentioning OnLimit:Fail", nodes[0].Error)
	}

	leaderRelease.Store(true)
	leaderRes := <-leaderDone
	if leaderRes.Status != "success" {
		t.Fatalf("leader status = %q, want success", leaderRes.Status)
	}
}

func TestCache_MemoizesAcrossRuns(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)

	res1, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-memoize"})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if res1.Status != "success" {
		t.Fatalf("run 1 status = %q", res1.Status)
	}
	if ran := cacheCounter.inflight.Load(); ran != 1 {
		t.Fatalf("run 1 body invocations = %d, want 1", ran)
	}

	res2, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-memoize"})
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res2.Status != "success" {
		t.Fatalf("run 2 status = %q", res2.Status)
	}
	if ran := cacheCounter.inflight.Load(); ran != 1 {
		t.Fatalf("run 2 body invocations (cumulative) = %d, want still 1", ran)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), res2.RunID)
	if len(nodes) != 1 {
		t.Fatalf("run 2: expected 1 node, got %d", len(nodes))
	}
	if nodes[0].Outcome != string(sparkwing.Cached) {
		t.Fatalf("run 2 node outcome = %q, want cached", nodes[0].Outcome)
	}
}

func TestConcurrency_DriftWarnEventEmitted(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)

	r1, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-drift-a"})
	if err != nil || r1.Status != "success" {
		t.Fatalf("run 1: status=%q err=%v", r1.Status, err)
	}
	r2, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-drift-b"})
	if err != nil || r2.Status != "success" {
		t.Fatalf("run 2: status=%q err=%v", r2.Status, err)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	events, _ := st.ListEventsAfter(context.Background(), r2.RunID, 0, 500)
	found := false
	for _, e := range events {
		if e.Kind == "concurrency_drift" {
			found = true
			if !strings.Contains(string(e.Payload), "cache-drift-key") {
				t.Errorf("drift event payload does not mention key: %s", e.Payload)
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected a concurrency_drift event in run 2's stream; got %d events", len(events))
	}
}

func outcomeSummary(nodes []*store.Node) map[string]int {
	m := map[string]int{}
	for _, n := range nodes {
		m[n.Outcome]++
	}
	return m
}

func TestConcurrency_PlanLevelQueueSerializesConcurrentRuns(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "plan-level-queue"})
		}()
	}
	wg.Wait()

	if peak := cacheCounter.max.Load(); peak > 1 {
		t.Fatalf("plan-level Queue cross-run peak concurrency = %d, want <= 1", peak)
	}
}

func TestConcurrency_PlanLevelInheritedAdmissionDoesNotQueueBehindParent(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	parentHolderID := "parent-run/-"
	resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "g:plan-level-inherited-key",
		HolderID: parentHolderID,
		RunID:    "parent-run",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("parent acquire: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("parent acquire = %s, want granted", resp.Kind)
	}

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	res, err := orchestrator.RunLocal(runCtx, p, orchestrator.Options{
		Pipeline:                         "plan-level-inherited-child",
		InheritedPlanConcurrencyKey:      "g:plan-level-inherited-key",
		InheritedPlanConcurrencyHolderID: parentHolderID,
	})
	if err != nil {
		t.Fatalf("child run with inherited admission: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("child status = %q, want success", res.Status)
	}
}

func TestConcurrency_RunAndAwaitPropagatesPlanAdmissionToChildTrigger(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)
	ctx := context.Background()

	res, err := orchestrator.RunLocal(ctx, p, orchestrator.Options{
		Pipeline: "plan-level-inherited-spawner",
		RunID:    "parent-with-plan-admission",
	})
	if err != nil {
		t.Fatalf("parent run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("parent status = %q, want failed from child timeout", res.Status)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	childID, err := st.FindSpawnedChildTriggerID(ctx, "parent-with-plan-admission", "spawn", "plan-level-inherited-child")
	if err != nil {
		t.Fatalf("FindSpawnedChildTriggerID: %v", err)
	}
	if childID == "" {
		t.Fatal("expected child trigger row")
	}
	trigger, err := st.GetTrigger(ctx, childID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_KEY"] != "g:plan-level-inherited-key" {
		t.Fatalf("child admission key = %q, want g:plan-level-inherited-key",
			trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_KEY"])
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_HOLDER_ID"] != "parent-with-plan-admission/-" {
		t.Fatalf("child admission holder = %q, want parent-with-plan-admission/-",
			trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_HOLDER_ID"])
	}
}

func TestConcurrency_RunAndAwaitCarriesInheritedAdmissionThroughPlanWithoutConcurrency(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	parentHolderID := "ancestor-with-plan-admission/-"
	resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "g:plan-level-inherited-key",
		HolderID: parentHolderID,
		RunID:    "ancestor-with-plan-admission",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("parent acquire: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("parent acquire = %s, want granted", resp.Kind)
	}

	res, err := orchestrator.RunLocal(ctx, p, orchestrator.Options{
		Pipeline:                         "plan-level-inherited-middle",
		RunID:                            "middle-without-plan-concurrency",
		InheritedPlanConcurrencyKey:      "g:plan-level-inherited-key",
		InheritedPlanConcurrencyHolderID: parentHolderID,
	})
	if err != nil {
		t.Fatalf("middle run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("middle status = %q, want failed from child timeout", res.Status)
	}

	childID, err := st.FindSpawnedChildTriggerID(ctx, "middle-without-plan-concurrency", "spawn", "plan-level-inherited-child")
	if err != nil {
		t.Fatalf("FindSpawnedChildTriggerID: %v", err)
	}
	if childID == "" {
		t.Fatal("expected grandchild trigger row")
	}
	trigger, err := st.GetTrigger(ctx, childID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_KEY"] != "g:plan-level-inherited-key" {
		t.Fatalf("grandchild admission key = %q, want g:plan-level-inherited-key",
			trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_KEY"])
	}
	if trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_HOLDER_ID"] != parentHolderID {
		t.Fatalf("grandchild admission holder = %q, want %q",
			trigger.TriggerEnv["SPARKWING_PLAN_ADMISSION_HOLDER_ID"], parentHolderID)
	}
}

func TestConcurrency_PlanLevelSkipShortCircuits(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)

	leaderDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "plan-level-skip-leader"})
		leaderDone <- res
	}()
	time.Sleep(100 * time.Millisecond)

	snapshotBefore := cacheCounter.inflight.Load()

	followerRes, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "plan-level-skip-follower",
	})
	if err != nil {
		t.Fatalf("follower: %v", err)
	}
	if followerRes.Status != "success" {
		t.Fatalf("follower status = %q, want success (Skip treats plan-level full slot as OK)", followerRes.Status)
	}

	<-leaderDone
	finalCount := cacheCounter.inflight.Load()
	if finalCount-snapshotBefore > 1 {
		t.Fatalf("too many step executions between snapshot and final (%d-%d), expected <= 1 (leader only)",
			finalCount, snapshotBefore)
	}
}

func TestConcurrency_CancelOthersEvictsCooperativeLeader(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)

	leaderDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
			Pipeline: "cache-cancel-others-leader",
		})
		leaderDone <- res
	}()
	time.Sleep(200 * time.Millisecond)

	followerStart := time.Now()
	followerRes, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "cache-cancel-others-follower",
	})
	followerElapsed := time.Since(followerStart)

	if followerRes.Status != "success" {
		t.Fatalf("follower status = %q, want success (evicted leader, took slot)", followerRes.Status)
	}
	if followerElapsed > 5*time.Second {
		t.Fatalf("follower took %s; expected eviction well under 5s", followerElapsed)
	}

	<-leaderDone
}
