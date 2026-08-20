package orchestrator_test

import (
	"context"
	"encoding/json"
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

var cacheCounter struct {
	inflight atomic.Int32
	max      atomic.Int32
}

type cacheStepGate struct {
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

var activeCacheStepGate atomic.Pointer[cacheStepGate]

func (g *cacheStepGate) letRun() {
	g.releaseOnce.Do(func() { close(g.release) })
}

func installCacheStepGate(t *testing.T) *cacheStepGate {
	t.Helper()
	gate := &cacheStepGate{started: make(chan struct{}), release: make(chan struct{})}
	if !activeCacheStepGate.CompareAndSwap(nil, gate) {
		t.Fatal("cache step gate already installed")
	}
	t.Cleanup(func() {
		gate.letRun()
		activeCacheStepGate.CompareAndSwap(gate, nil)
	})
	return gate
}

func cacheStep() func(ctx context.Context) error {
	return func(ctx context.Context) error {
		cur := cacheCounter.inflight.Add(1)
		defer cacheCounter.inflight.Add(-1)
		for {
			peak := cacheCounter.max.Load()
			if cur <= peak || cacheCounter.max.CompareAndSwap(peak, cur) {
				break
			}
		}
		gate := activeCacheStepGate.Load()
		if gate == nil {
			return nil
		}
		gate.startedOnce.Do(func() { close(gate.started) })
		select {
		case <-gate.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type cacheQueuePipe struct{ sparkwing.Base }

func (cacheQueuePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-queue-key", sparkwing.ConcurrencyLimit{Capacity: 1})
	sparkwing.Job(plan, "a", cacheStep()).Concurrency(g)
	sparkwing.Job(plan, "b", cacheStep()).Concurrency(g)
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
	sparkwing.Job(plan, "follower", cacheStep()).Concurrency(g)
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
	sparkwing.Job(plan, "follower", cacheStep()).Concurrency(g)
	return nil
}

type cacheCancelOthersLeaderPipe struct{ sparkwing.Base }

func (cacheCancelOthersLeaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-cancel-others-key", sparkwing.ConcurrencyLimit{Capacity: 1})
	sparkwing.Job(plan, "leader", held(nil)).Concurrency(g)
	return nil
}

type cacheCancelOthersFollowerPipe struct{ sparkwing.Base }

func (cacheCancelOthersFollowerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-cancel-others-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.CancelOthers,
	})
	sparkwing.Job(plan, "follower", cacheStep()).Concurrency(g)
	return nil
}

type cacheForcedReleaseLeaderPipe struct{ sparkwing.Base }

func (cacheForcedReleaseLeaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-forced-release-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.CancelOthers,
	})
	sparkwing.Job(plan, "leader", held(nil)).Concurrency(g)
	return nil
}

type cacheForcedReleaseFollowerPipe struct{ sparkwing.Base }

func (cacheForcedReleaseFollowerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-forced-release-key", sparkwing.ConcurrencyLimit{
		Capacity:      1,
		OnLimit:       sparkwing.CancelOthers,
		CancelTimeout: 100 * time.Millisecond,
	})
	sparkwing.Job(plan, "follower", cacheStep()).Concurrency(g)
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
	}).Memoize(
		func(ctx context.Context) sparkwing.CacheKey { return "v-pinned" },
		sparkwing.TTL(time.Hour))
	return nil
}

// cacheDriftPipe declares the same group name with different capacities
// across two runs so the second run's acquire records a capacity drift.
type cacheDriftPipeA struct{ sparkwing.Base }

func (cacheDriftPipeA) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-drift-key", sparkwing.ConcurrencyLimit{Capacity: 1})
	sparkwing.Job(plan, "a", cacheStep()).Concurrency(g)
	return nil
}

type cacheDriftPipeB struct{ sparkwing.Base }

func (cacheDriftPipeB) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cache-drift-key", sparkwing.ConcurrencyLimit{Capacity: 3})
	sparkwing.Job(plan, "a", cacheStep()).Concurrency(g)
	return nil
}

// planLevelQueuePipe: single-node plan gated by Plan.Concurrency at
// capacity 1. Running two concurrently MUST serialize -- peak
// concurrency across both runs' nodes should stay at 1.
type planLevelQueuePipe struct{ sparkwing.Base }

func (planLevelQueuePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-key", sparkwing.ConcurrencyLimit{Capacity: 1}))
	sparkwing.Job(plan, "work", cacheStep())
	return nil
}

type planLevelCancelOthersPipe struct{ sparkwing.Base }

func (planLevelCancelOthersPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-cancel-others-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.CancelOthers,
	}))
	sparkwing.Job(plan, "work", held(nil))
	return nil
}

type planLevelCancelOthersQuickPipe struct{ sparkwing.Base }

func (planLevelCancelOthersQuickPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-cancel-others-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.CancelOthers,
	}))
	sparkwing.Job(plan, "work", func(context.Context) error { return nil })
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
	sparkwing.Job(plan, "work", cacheStep())
	return nil
}

type planLevelSkipLeaderPipe struct{ sparkwing.Base }

func (planLevelSkipLeaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-skip-key", sparkwing.ConcurrencyLimit{Capacity: 1}))
	sparkwing.Job(plan, "work", held(cacheCounterBump))
	return nil
}

type planLevelQueuedAwaitParentPipe struct{ sparkwing.Base }

const admissionPauseFixtureTimeout = 750 * time.Millisecond

type queuedAwaitParentGate struct {
	started chan context.Context
	proceed chan struct{}
	once    sync.Once
}

func (g *queuedAwaitParentGate) release() {
	if g == nil || g.proceed == nil {
		return
	}
	g.once.Do(func() { close(g.proceed) })
}

var queuedAwaitParentAttempt atomic.Pointer[queuedAwaitParentGate]

func publishQueuedAwaitParentAttempt(ctx context.Context) {
	if gate := queuedAwaitParentAttempt.Load(); gate != nil {
		select {
		case gate.started <- ctx:
		default:
		}
		if gate.proceed != nil {
			select {
			case <-gate.proceed:
			case <-ctx.Done():
			}
		}
	}
}

func (planLevelQueuedAwaitParentPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		publishQueuedAwaitParentAttempt(ctx)
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](ctx, "plan-level-queued-await-child", "work")
		return err
	}).NoProgressTimeout(100 * time.Millisecond).Timeout(admissionPauseFixtureTimeout)
	return nil
}

type unboundedAwaitParentPipe struct{ sparkwing.Base }

func (unboundedAwaitParentPipe) Plan(
	_ context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	_ sparkwing.RunContext,
) error {
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		publishQueuedAwaitParentAttempt(ctx)
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](ctx, "plan-level-queued-await-child", "work")
		return err
	})
	return nil
}

type explicitTimeoutAwaitParentPipe struct{ sparkwing.Base }

func (explicitTimeoutAwaitParentPipe) Plan(
	_ context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	_ sparkwing.RunContext,
) error {
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		publishQueuedAwaitParentAttempt(ctx)
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](
			ctx,
			"plan-level-queued-await-child",
			"work",
			sparkwing.WithFreshTimeout(3*time.Second),
		)
		return err
	})
	return nil
}

type planLevelQueuedAwaitThenContinueParentPipe struct{ sparkwing.Base }

func (planLevelQueuedAwaitThenContinueParentPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		publishQueuedAwaitParentAttempt(ctx)
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](ctx, "plan-level-queued-await-child", "work")
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}).NoProgressTimeout(time.Hour).Timeout(admissionPauseFixtureTimeout)
	return nil
}

type planLevelQueuedAwaitChildPipe struct{ sparkwing.Base }

func (planLevelQueuedAwaitChildPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-queued-await-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	}))
	sparkwing.Job(plan, "work", cacheStep())
	return nil
}

type planLevelQueuedAwaitRemainingBudgetParentPipe struct{ sparkwing.Base }

func (planLevelQueuedAwaitRemainingBudgetParentPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		publishQueuedAwaitParentAttempt(ctx)
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](ctx, "plan-level-queued-await-remaining-budget-child", "work")
		return err
	}).Timeout(time.Hour)
	return nil
}

type planLevelQueuedAwaitRemainingBudgetChildPipe struct{ sparkwing.Base }

func (planLevelQueuedAwaitRemainingBudgetChildPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-queued-await-remaining-budget-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	}))
	sparkwing.Job(plan, "work", cacheStep())
	return nil
}

type planLevelQueuedAwaitEarlyResumeParentPipe struct{ sparkwing.Base }

func (planLevelQueuedAwaitEarlyResumeParentPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		publishQueuedAwaitParentAttempt(ctx)
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](ctx, "plan-level-queued-await-early-resume-child", "work")
		return err
	}).Timeout(time.Hour)
	return nil
}

type planLevelQueuedAwaitEarlyResumeChildPipe struct{ sparkwing.Base }

func (planLevelQueuedAwaitEarlyResumeChildPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-queued-await-early-resume-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	}))
	sparkwing.Job(plan, "work", cacheStep())
	return nil
}

type planLevelQueuedAwaitMissedPromotionParentPipe struct{ sparkwing.Base }

func (planLevelQueuedAwaitMissedPromotionParentPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		publishQueuedAwaitParentAttempt(ctx)
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](ctx, "plan-level-queued-await-missed-promotion-child", "work")
		return err
	}).Timeout(700 * time.Millisecond)
	return nil
}

type planLevelQueuedAwaitMissedPromotionChildPipe struct{ sparkwing.Base }

func (planLevelQueuedAwaitMissedPromotionChildPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-queued-await-missed-promotion-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	}))
	sparkwing.Job(plan, "work", cacheStep())
	return nil
}

type planLevelQueuedAwaitMultiKeyParentPipe struct{ sparkwing.Base }

func (planLevelQueuedAwaitMultiKeyParentPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		publishQueuedAwaitParentAttempt(ctx)
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](ctx, "plan-level-queued-await-multi-key-child", "work")
		return err
	}).Timeout(500 * time.Millisecond)
	return nil
}

type planLevelQueuedAwaitMultiKeyChildPipe struct{ sparkwing.Base }

func (planLevelQueuedAwaitMultiKeyChildPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-queued-await-multi-key-a", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	}))
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-queued-await-multi-key-b", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	}))
	sparkwing.Job(plan, "work", cacheStep())
	return nil
}

type planLevelSlowPlanAwaitParentPipe struct{ sparkwing.Base }

func (planLevelSlowPlanAwaitParentPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	sparkwing.Job(plan, "spawn", func(ctx context.Context) error {
		_, err := sparkwing.RunAndAwait[struct{}, sparkwing.NoInputs](ctx, "plan-level-slow-plan-await-child", "work")
		return err
	}).Timeout(150 * time.Millisecond)
	return nil
}

type planLevelSlowPlanAwaitChildPipe struct{ sparkwing.Base }

func (planLevelSlowPlanAwaitChildPipe) Plan(
	ctx context.Context,
	plan *sparkwing.Plan,
	_ sparkwing.NoInputs,
	rc sparkwing.RunContext,
) error {
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	plan.Concurrency(sparkwing.NewConcurrencyGroup("plan-level-slow-plan-await-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	}))
	sparkwing.Job(plan, "work", cacheStep())
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
	register("cache-forced-release-leader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheForcedReleaseLeaderPipe{} })
	register("cache-forced-release-follower", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheForcedReleaseFollowerPipe{} })
	register("cache-memoize", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheKeyedPipe{} })
	register("cache-drift-a", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheDriftPipeA{} })
	register("cache-drift-b", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cacheDriftPipeB{} })
	register("plan-level-queue", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &planLevelQueuePipe{} })
	register("plan-level-cancel-others", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &planLevelCancelOthersPipe{} })
	register("plan-level-cancel-others-quick", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelCancelOthersQuickPipe{}
	})
	register("plan-level-skip-leader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &planLevelSkipLeaderPipe{} })
	register("plan-level-skip-follower", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &planLevelSkipFollowerPipe{} })
	register("plan-level-queued-await-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitParentPipe{}
	})
	register("unbounded-await-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &unboundedAwaitParentPipe{}
	})
	register("explicit-timeout-await-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &explicitTimeoutAwaitParentPipe{}
	})
	register("plan-level-queued-await-then-continue-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitThenContinueParentPipe{}
	})
	register("plan-level-queued-await-child", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitChildPipe{}
	})
	register("plan-level-queued-await-remaining-budget-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitRemainingBudgetParentPipe{}
	})
	register("plan-level-queued-await-remaining-budget-child", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitRemainingBudgetChildPipe{}
	})
	register("plan-level-queued-await-early-resume-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitEarlyResumeParentPipe{}
	})
	register("plan-level-queued-await-early-resume-child", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitEarlyResumeChildPipe{}
	})
	register("plan-level-queued-await-missed-promotion-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitMissedPromotionParentPipe{}
	})
	register("plan-level-queued-await-missed-promotion-child", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitMissedPromotionChildPipe{}
	})
	register("plan-level-queued-await-multi-key-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitMultiKeyParentPipe{}
	})
	register("plan-level-queued-await-multi-key-child", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelQueuedAwaitMultiKeyChildPipe{}
	})
	register("plan-level-slow-plan-await-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelSlowPlanAwaitParentPipe{}
	})
	register("plan-level-slow-plan-await-child", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &planLevelSlowPlanAwaitChildPipe{}
	})
}

func resetCacheCounter() {
	cacheCounter.inflight.Store(0)
	cacheCounter.max.Store(0)
	resetLeaderBarrier()
}

func claimManualChildTrigger(t *testing.T, ctx context.Context, st *store.Store, childID string) {
	t.Helper()
	trigger, err := st.ClaimSpecificTrigger(ctx, childID, store.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("claim child trigger %q for manual run: %v", childID, err)
	}
	if trigger.ID != childID {
		t.Fatalf("claimed trigger = %q, want %q", trigger.ID, childID)
	}
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
	gate := installCacheStepGate(t)
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan *orchestrator.Result, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		gate.letRun()
		cancel()
		joinCacheDispatchWorker(t, "single-run cache serialization", finished)
	})
	go func() {
		defer close(finished)
		res, _ := orchestrator.RunLocal(ctx, p, orchestrator.Options{Pipeline: "cache-queue-serialize", RunID: "cache-queue-single"})
		done <- res
	}()
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("first cache body did not start")
	}
	waitForCacheConcurrencyPopulation(t, ctx, p.StateDB(), "g:cache-queue-key", 1, 1)
	gate.letRun()
	var res *orchestrator.Result
	select {
	case res = <-done:
	case <-ctx.Done():
		t.Fatal("cache serialization run did not finish")
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
	gate := installCacheStepGate(t)
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan *orchestrator.Result, 2)
	finished := make(chan struct{})
	var wg sync.WaitGroup
	for index := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, _ := orchestrator.RunLocal(ctx, p, orchestrator.Options{Pipeline: "cache-queue-serialize", RunID: fmt.Sprintf("cache-queue-run-%d", index)})
			done <- res
		}()
	}
	go func() {
		wg.Wait()
		close(finished)
	}()
	t.Cleanup(func() {
		gate.letRun()
		cancel()
		joinCacheDispatchWorker(t, "cross-run cache serialization", finished)
	})
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("first cross-run cache body did not start")
	}
	waitForCacheConcurrencyPopulation(t, ctx, p.StateDB(), "g:cache-queue-key", 1, 3)
	gate.letRun()
	for range 2 {
		select {
		case res := <-done:
			if res.Status != "success" {
				t.Fatalf("cross-run status = %q, want success", res.Status)
			}
		case <-ctx.Done():
			t.Fatal("cross-run cache serialization did not finish")
		}
	}

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
	var waitLeaderOnce sync.Once
	var leaderRes *orchestrator.Result
	waitLeader := func() *orchestrator.Result {
		releaseLeaderBarrier()
		waitLeaderOnce.Do(func() { leaderRes = <-leaderDone })
		return leaderRes
	}
	t.Cleanup(func() { _ = waitLeader() })

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

	releaseLeaderBarrier()
	leaderRes = waitLeader()
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

	releaseLeaderBarrier()
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

func waitForConcurrencyHolder(t *testing.T, dbPath, holderID string) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	deadlineAt := time.Now().Add(15 * time.Second)
	pollCtx, cancel := context.WithDeadline(context.Background(), deadlineAt)
	defer cancel()
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for time.Now().Before(deadlineAt) {
		var count int
		err := st.DB().QueryRowContext(pollCtx,
			`SELECT COUNT(*) FROM concurrency_holders WHERE holder_id = ?`,
			holderID,
		).Scan(&count)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(pollCtx.Err(), context.DeadlineExceeded) {
				t.Fatalf("timed out waiting for concurrency holder %q", holderID)
			}
			t.Fatalf("query concurrency holder %q: %v", holderID, err)
		}
		if count > 0 {
			return
		}
		poll.Reset(2 * time.Millisecond)
		select {
		case <-poll.C:
		case <-pollCtx.Done():
			t.Fatalf("timed out waiting for concurrency holder %q", holderID)
		}
	}
	t.Fatalf("timed out waiting for concurrency holder %q", holderID)
}

func waitForSpawnedChildTrigger(t *testing.T, ctx context.Context, st *store.Store, parentRunID, parentNodeID, pipeline string) string {
	t.Helper()
	pollCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		childID, err := st.FindSpawnedChildTriggerID(pollCtx, parentRunID, parentNodeID, pipeline)
		if err != nil {
			if pollCtx.Err() != nil {
				t.Fatalf("waiting for child trigger: %v", pollCtx.Err())
			}
			t.Fatalf("find spawned child trigger: %v", err)
		}
		if childID != "" {
			return childID
		}
		poll.Reset(10 * time.Millisecond)
		select {
		case <-poll.C:
		case <-pollCtx.Done():
			t.Fatalf("waiting for child trigger: %v", pollCtx.Err())
		}
	}
}

func waitForPlanAdmissionWaiter(t *testing.T, ctx context.Context, st *store.Store, key, runID string, childDone <-chan *orchestrator.Result) {
	t.Helper()
	pollCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		state, err := st.GetConcurrencyState(pollCtx, key)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			if pollCtx.Err() != nil {
				t.Fatalf("waiting for run %q to queue for %s: %v", runID, key, pollCtx.Err())
			}
			t.Fatalf("get concurrency state for %s: %v", key, err)
		}
		if err == nil {
			for _, waiter := range state.Waiters {
				if waiter.RunID == runID && waiter.NodeID == "" {
					return
				}
			}
		}
		poll.Reset(10 * time.Millisecond)
		select {
		case child := <-childDone:
			t.Fatalf("child finished before queuing for admission: status=%q err=%v", child.Status, child.Error)
		case <-poll.C:
		case <-pollCtx.Done():
			t.Fatalf("waiting for run %q to queue for %s: %v", runID, key, pollCtx.Err())
		}
	}
}

func waitForCacheConcurrencyPopulation(t *testing.T, ctx context.Context, dbPath, key string, holders, waiters int) {
	t.Helper()
	pollCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open concurrency store: %v", err)
	}
	defer func() { _ = st.Close() }()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		state, err := st.GetConcurrencyState(pollCtx, key)
		if err == nil && len(state.Holders) == holders && len(state.Waiters) == waiters {
			return
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			if pollCtx.Err() != nil {
				t.Fatalf("waiting for concurrency population on %q: %v", key, pollCtx.Err())
			}
			t.Fatalf("read concurrency population on %q: %v", key, err)
		}
		poll.Reset(10 * time.Millisecond)
		select {
		case <-poll.C:
		case <-pollCtx.Done():
			t.Fatalf("timed out waiting for %d holders and %d waiters on %q", holders, waiters, key)
		}
	}
}

func waitForCacheEvent(t *testing.T, ctx context.Context, dbPath, runID, kind string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store while waiting for %s: %v", kind, err)
	}
	defer func() { _ = st.Close() }()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		events, err := st.ListEventsAfter(ctx, runID, 0, 500)
		if err != nil {
			t.Fatalf("list %s events while waiting for %s: %v", runID, kind, err)
		}
		for _, event := range events {
			if event.Kind == kind {
				return
			}
		}
		poll.Reset(10 * time.Millisecond)
		select {
		case <-poll.C:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s event for %s", kind, runID)
		}
	}
}

func joinCacheDispatchWorker(t *testing.T, name string, done <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Errorf("%s did not stop within 2s", name)
	}
}

func waitForNodeTimeoutPaused(t *testing.T, attemptCtx context.Context) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for {
		if orchestrator.NodeTimeoutPausedForTest(attemptCtx) {
			return
		}
		if err := attemptCtx.Err(); err != nil {
			t.Fatalf("parent action ended before its node timeout paused: %v", err)
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("parent node timeout did not pause during child admission")
		}
	}
}

func waitForNodeTimeoutResumed(t *testing.T, attemptCtx context.Context) time.Duration {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for {
		remaining, paused, ok := orchestrator.NodeTimeoutStateForTest(attemptCtx)
		if ok && !paused {
			return remaining
		}
		if err := attemptCtx.Err(); err != nil {
			t.Fatalf("parent action ended before its node timeout resumed: %v", err)
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("parent node timeout did not resume after child admission")
		}
	}
}

func waitForProgressTimeoutResumed(t *testing.T, attemptCtx context.Context) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for {
		if !orchestrator.ProgressTimeoutPausedForTest(attemptCtx) {
			if err := attemptCtx.Err(); err != nil {
				t.Fatalf("parent action ended before its progress timeout resumed: %v", err)
			}
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("parent progress timeout did not resume after child admission")
		}
	}
}

func TestConcurrency_PlanLevelQueueSerializesConcurrentRuns(t *testing.T) {
	resetCacheCounter()
	gate := installCacheStepGate(t)
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan *orchestrator.Result, 2)
	finished := make(chan struct{})
	var wg sync.WaitGroup
	for index := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, _ := orchestrator.RunLocal(ctx, p, orchestrator.Options{Pipeline: "plan-level-queue", RunID: fmt.Sprintf("plan-queue-run-%d", index)})
			done <- res
		}()
	}
	go func() {
		wg.Wait()
		close(finished)
	}()
	t.Cleanup(func() {
		gate.letRun()
		cancel()
		joinCacheDispatchWorker(t, "plan-level cache serialization", finished)
	})
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("first plan-level cache body did not start")
	}
	waitForCacheConcurrencyPopulation(t, ctx, p.StateDB(), "g:plan-level-key", 1, 1)
	gate.letRun()
	for range 2 {
		select {
		case res := <-done:
			if res.Status != "success" {
				t.Fatalf("plan-level status = %q, want success", res.Status)
			}
		case <-ctx.Done():
			t.Fatal("plan-level cache serialization did not finish")
		}
	}

	if peak := cacheCounter.max.Load(); peak > 1 {
		t.Fatalf("plan-level Queue cross-run peak concurrency = %d, want <= 1", peak)
	}
}

func TestConcurrency_PlanLevelQueueEmitsAdmissionEvents(t *testing.T) {
	resetCacheCounter()
	gate := installCacheStepGate(t)
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	leaderDone := make(chan *orchestrator.Result, 1)
	leaderFinished := make(chan struct{})
	followerDone := make(chan *orchestrator.Result, 1)
	followerFinished := make(chan struct{})
	var followerStarted atomic.Bool
	t.Cleanup(func() {
		gate.letRun()
		cancel()
		joinCacheDispatchWorker(t, "plan queue event leader", leaderFinished)
		if followerStarted.Load() {
			joinCacheDispatchWorker(t, "plan queue event follower", followerFinished)
		}
	})
	go func() {
		defer close(leaderFinished)
		res, _ := orchestrator.RunLocal(ctx, p, orchestrator.Options{
			Pipeline: "plan-level-queue",
			RunID:    "plan-queue-leader",
		})
		leaderDone <- res
	}()
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("plan queue event leader body did not start")
	}
	waitForConcurrencyHolder(t, p.StateDB(), "plan-queue-leader/-")

	followerStarted.Store(true)
	go func() {
		defer close(followerFinished)
		res, _ := orchestrator.RunLocal(ctx, p, orchestrator.Options{
			Pipeline: "plan-level-queue",
			RunID:    "plan-queue-follower",
		})
		followerDone <- res
	}()
	waitForCacheConcurrencyPopulation(t, ctx, p.StateDB(), "g:plan-level-key", 1, 1)
	waitForCacheEvent(t, ctx, p.StateDB(), "plan-queue-follower", "concurrency_wait_update")
	gate.letRun()
	var follower *orchestrator.Result
	select {
	case follower = <-followerDone:
	case <-ctx.Done():
		t.Fatal("plan queue event follower did not finish")
	}
	if follower.Status != "success" {
		t.Fatalf("follower status = %q, want success", follower.Status)
	}
	select {
	case leader := <-leaderDone:
		if leader.Status != "success" {
			t.Fatalf("leader status = %q, want success", leader.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for leader")
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	events, err := st.ListEventsAfter(context.Background(), "plan-queue-follower", 0, 500)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	want := map[string]bool{
		"concurrency_wait":        false,
		"concurrency_wait_update": false,
		"concurrency_promoted":    false,
	}
	for _, event := range events {
		if _, ok := want[event.Kind]; !ok {
			continue
		}
		want[event.Kind] = true
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("%s payload: %v", event.Kind, err)
		}
		if payload["scope"] != "plan" || payload["key"] != "g:plan-level-key" || payload["resource"] != "plan_admission" {
			t.Fatalf("%s payload = %+v", event.Kind, payload)
		}
		if event.Kind != "concurrency_promoted" && payload["holders"] == nil {
			t.Fatalf("%s payload missing holders: %+v", event.Kind, payload)
		}
	}
	for kind, found := range want {
		if !found {
			t.Fatalf("missing %s event in %+v", kind, events)
		}
	}
}

func TestConcurrency_PlanLevelEvictedBeforeDispatchCancelsRun(t *testing.T) {
	resetLeaderBarrier()
	p := newPaths(t)

	leaderDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
			Pipeline: "plan-level-cancel-others",
			RunID:    "plan-cancel-leader",
		})
		leaderDone <- res
	}()
	waitForLeaderHolding(t)

	victimDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
			Pipeline: "plan-level-cancel-others-quick",
			RunID:    "plan-cancel-victim",
		})
		victimDone <- res
	}()
	waitForConcurrencyHolder(t, p.StateDB(), "plan-cancel-victim/-")

	evictor, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "plan-level-cancel-others-quick",
		RunID:    "plan-cancel-evictor",
	})
	if err != nil {
		t.Fatalf("evictor: %v", err)
	}
	if evictor.Status != "success" {
		t.Fatalf("evictor status = %q, want success", evictor.Status)
	}

	var victim *orchestrator.Result
	select {
	case victim = <-victimDone:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for pre-dispatch evicted run to finish")
	}
	if victim.Status != "cancelled" {
		t.Fatalf("victim status = %q, want cancelled for pre-dispatch admission eviction (err=%v)", victim.Status, victim.Error)
	}
}

func TestConcurrency_RunAndAwaitParentTimeoutDoesNotCountChildPlanAdmissionWait(t *testing.T) {
	testRunAndAwaitAdmissionOutlivesDispatchWatchdog(t, "plan-level-queued-await-parent", 100*time.Millisecond)
}

func TestConcurrency_RunAndAwaitExplicitTimeoutProtectsParentDispatch(t *testing.T) {
	testRunAndAwaitAdmissionOutlivesDispatchWatchdog(t, "explicit-timeout-await-parent", 100*time.Millisecond)
}

func TestConcurrency_RunAndAwaitUnboundedClaimedChildAdmissionProtectsParentDispatch(t *testing.T) {
	testRunAndAwaitAdmissionOutlivesDispatchWatchdog(t, "unbounded-await-parent", 750*time.Millisecond)
}

func observeAdmissionWaitBeyondDispatchTimeout(
	t *testing.T,
	attemptCtx context.Context,
	parentDone <-chan *orchestrator.Result,
	dispatchTimeout time.Duration,
) {
	t.Helper()
	deadline := time.NewTimer(dispatchTimeout + 100*time.Millisecond)
	defer deadline.Stop()
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for {
		if !orchestrator.AdmissionWaitActiveForTest(attemptCtx) {
			t.Fatal("parent admission wait ended before the dispatch-watchdog observation completed")
		}
		select {
		case parent := <-parentDone:
			t.Fatalf("parent finished during its admission wait: status=%q err=%v", parent.Status, parent.Error)
		case <-deadline.C:
			if !orchestrator.AdmissionWaitActiveForTest(attemptCtx) {
				t.Fatal("parent admission wait ended at the dispatch-watchdog boundary")
			}
			return
		case <-poll.C:
		}
	}
}

func testRunAndAwaitAdmissionOutlivesDispatchWatchdog(t *testing.T, parentPipeline string, dispatchTimeout time.Duration) {
	t.Helper()
	resetCacheCounter()
	gate := &queuedAwaitParentGate{started: make(chan context.Context, 1)}
	queuedAwaitParentAttempt.Store(gate)
	t.Cleanup(func() { queuedAwaitParentAttempt.CompareAndSwap(gate, nil) })
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "g:plan-level-queued-await-key",
		HolderID: "external-plan-holder/-",
		RunID:    "external-plan-holder",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("external acquire: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("external acquire = %s, want granted", resp.Kind)
	}

	parentDone := make(chan *orchestrator.Result, 1)
	parentFinished := make(chan struct{})
	childDone := make(chan *orchestrator.Result, 1)
	childFinished := make(chan struct{})
	var childStarted atomic.Bool
	var releaseOnce sync.Once
	var releaseErr error
	releaseHolder := func() error {
		releaseOnce.Do(func() {
			_, _, _, releaseErr = st.ReleaseAndNotify(context.Background(),
				"g:plan-level-queued-await-key", "external-plan-holder/-", "success", "", "", 0, store.DefaultConcurrencyLease)
		})
		return releaseErr
	}
	t.Cleanup(func() {
		gate.release()
		cancel()
		if err := releaseHolder(); err != nil {
			t.Errorf("cleanup external holder: %v", err)
		}
		joinCacheDispatchWorker(t, "dispatch-watchdog parent", parentFinished)
		if childStarted.Load() {
			joinCacheDispatchWorker(t, "dispatch-watchdog child", childFinished)
		}
	})
	go func() {
		defer close(parentFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline:            parentPipeline,
			RunID:               "queued-await-parent",
			DispatchWaitTimeout: dispatchTimeout,
		})
		parentDone <- res
	}()

	childID := waitForSpawnedChildTrigger(t, ctx, st, "queued-await-parent", "spawn", "plan-level-queued-await-child")
	claimManualChildTrigger(t, ctx, st, childID)
	childStarted.Store(true)
	go func() {
		defer close(childFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline:    "plan-level-queued-await-child",
			RunID:       childID,
			ParentRunID: "queued-await-parent",
		})
		childDone <- res
	}()
	waitForPlanAdmissionWaiter(t, ctx, st, "g:plan-level-queued-await-key", childID, childDone)
	var attemptCtx context.Context
	select {
	case attemptCtx = <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("parent action did not publish its admission context")
	}
	observeAdmissionWaitBeyondDispatchTimeout(t, attemptCtx, parentDone, dispatchTimeout)

	if err := releaseHolder(); err != nil {
		t.Fatalf("release external holder: %v", err)
	}

	select {
	case parent := <-parentDone:
		if parent.Status != "success" {
			t.Fatalf("parent status = %q, want success after child admission (err=%v)", parent.Status, parent.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for parent after releasing queued child")
	}
	select {
	case child := <-childDone:
		if child.Status != "success" {
			t.Fatalf("child status = %q, want success (err=%v)", child.Status, child.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child after releasing queued child")
	}
}

func TestDispatchWatchdog_UnclaimedUnboundedChildStillTimesOutParent(t *testing.T) {
	p := newPaths(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(ctx, p, orchestrator.Options{
			Pipeline:            "unbounded-await-parent",
			DispatchWaitTimeout: 100 * time.Millisecond,
		})
		done <- res
	}()

	select {
	case res := <-done:
		if res == nil || res.Status != "failed" || res.Error == nil || !strings.Contains(res.Error.Error(), "dispatch_wait_timeout") {
			t.Fatalf("result = %+v, want dispatch_wait_timeout for unclaimed unbounded child", res)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("unclaimed unbounded child disabled the parent dispatch watchdog")
	}
}

func TestConcurrency_RunAndAwaitNoProgressTimeoutResumesAfterAdmissionWait(t *testing.T) {
	resetCacheCounter()
	gate := &queuedAwaitParentGate{started: make(chan context.Context, 1)}
	queuedAwaitParentAttempt.Store(gate)
	t.Cleanup(func() { queuedAwaitParentAttempt.CompareAndSwap(gate, nil) })
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "g:plan-level-queued-await-key",
		HolderID: "external-continue-holder/-",
		RunID:    "external-continue-holder",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("external acquire: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("external acquire = %s, want granted", resp.Kind)
	}

	parentDone := make(chan *orchestrator.Result, 1)
	parentFinished := make(chan struct{})
	childDone := make(chan *orchestrator.Result, 1)
	childFinished := make(chan struct{})
	var childStarted atomic.Bool
	var releaseOnce sync.Once
	var releaseErr error
	releaseHolder := func() error {
		releaseOnce.Do(func() {
			_, _, _, releaseErr = st.ReleaseAndNotify(context.Background(),
				"g:plan-level-queued-await-key", "external-continue-holder/-", "success", "", "", 0, store.DefaultConcurrencyLease)
		})
		return releaseErr
	}
	t.Cleanup(func() {
		gate.release()
		cancel()
		if err := releaseHolder(); err != nil {
			t.Errorf("cleanup external holder: %v", err)
		}
		joinCacheDispatchWorker(t, "no-progress parent", parentFinished)
		if childStarted.Load() {
			joinCacheDispatchWorker(t, "no-progress child", childFinished)
		}
	})
	go func() {
		defer close(parentFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline: "plan-level-queued-await-then-continue-parent",
			RunID:    "queued-await-continue-parent",
		})
		parentDone <- res
	}()

	childID := waitForSpawnedChildTrigger(t, ctx, st, "queued-await-continue-parent", "spawn", "plan-level-queued-await-child")
	claimManualChildTrigger(t, ctx, st, childID)
	childStarted.Store(true)
	go func() {
		defer close(childFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline:    "plan-level-queued-await-child",
			RunID:       childID,
			ParentRunID: "queued-await-continue-parent",
		})
		childDone <- res
	}()
	waitForPlanAdmissionWaiter(t, ctx, st, "g:plan-level-queued-await-key", childID, childDone)
	var attemptCtx context.Context
	select {
	case attemptCtx = <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("parent action did not publish its progress timeout context")
	}
	if !orchestrator.ProgressTimeoutPausedForTest(attemptCtx) {
		t.Fatal("parent progress timeout was not paused during child admission")
	}
	if orchestrator.ForceProgressTimeoutForTest(attemptCtx) {
		t.Fatal("parent progress timeout fired while child admission was pending")
	}
	if err := attemptCtx.Err(); err != nil {
		t.Fatalf("parent action ended before child admission: %v", err)
	}
	select {
	case parent := <-parentDone:
		t.Fatalf("parent finished while child was queued for plan admission: status=%q err=%v", parent.Status, parent.Error)
	default:
	}
	if err := releaseHolder(); err != nil {
		t.Fatalf("release external holder: %v", err)
	}
	waitForProgressTimeoutResumed(t, attemptCtx)
	if !orchestrator.ForceProgressTimeoutForTest(attemptCtx) {
		t.Fatal("resumed parent progress timeout could not be forced")
	}

	select {
	case parent := <-parentDone:
		if parent.Status != "failed" {
			t.Fatalf("parent status = %q, want no-progress failure after child wait (err=%v)", parent.Status, parent.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for parent after releasing queued child")
	}
	parentNodes, err := st.ListNodes(ctx, "queued-await-continue-parent")
	if err != nil {
		t.Fatalf("list parent nodes: %v", err)
	}
	if len(parentNodes) != 1 || parentNodes[0].FailureReason != store.FailureNoProgressTimeout {
		t.Fatalf("parent nodes = %+v, want failure reason %q", parentNodes, store.FailureNoProgressTimeout)
	}
	select {
	case child := <-childDone:
		if child.Status != "success" {
			t.Fatalf("child status = %q, want success (err=%v)", child.Status, child.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child after releasing queued child")
	}
}

func TestConcurrency_RunAndAwaitParentCancellationWhileAdmissionTimeoutPaused(t *testing.T) {
	resetCacheCounter()
	gate := &queuedAwaitParentGate{started: make(chan context.Context, 1)}
	queuedAwaitParentAttempt.Store(gate)
	t.Cleanup(func() { queuedAwaitParentAttempt.CompareAndSwap(gate, nil) })
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	resp, err := st.AcquireConcurrencySlot(context.Background(), store.AcquireSlotRequest{
		Key:      "g:plan-level-queued-await-key",
		HolderID: "external-plan-holder/-",
		RunID:    "external-plan-holder",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("external acquire: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("external acquire = %s, want granted", resp.Kind)
	}

	parentDone := make(chan *orchestrator.Result, 1)
	parentFinished := make(chan struct{})
	childDone := make(chan *orchestrator.Result, 1)
	childFinished := make(chan struct{})
	var childStarted atomic.Bool
	var releaseOnce sync.Once
	var releaseErr error
	releaseHolder := func() error {
		releaseOnce.Do(func() {
			_, _, _, releaseErr = st.ReleaseAndNotify(context.Background(),
				"g:plan-level-queued-await-key", "external-plan-holder/-", "success", "", "", 0, store.DefaultConcurrencyLease)
		})
		return releaseErr
	}
	t.Cleanup(func() {
		gate.release()
		cancel()
		if err := releaseHolder(); err != nil {
			t.Errorf("cleanup external holder: %v", err)
		}
		joinCacheDispatchWorker(t, "admission-cancellation parent", parentFinished)
		if childStarted.Load() {
			joinCacheDispatchWorker(t, "admission-cancellation child", childFinished)
		}
	})
	go func() {
		defer close(parentFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline: "plan-level-queued-await-parent",
			RunID:    "queued-await-cancel-parent",
		})
		parentDone <- res
	}()

	childID := waitForSpawnedChildTrigger(t, context.Background(), st, "queued-await-cancel-parent", "spawn", "plan-level-queued-await-child")
	claimManualChildTrigger(t, context.Background(), st, childID)
	childStarted.Store(true)
	go func() {
		defer close(childFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline:    "plan-level-queued-await-child",
			RunID:       childID,
			ParentRunID: "queued-await-cancel-parent",
		})
		childDone <- res
	}()
	waitForPlanAdmissionWaiter(t, context.Background(), st, "g:plan-level-queued-await-key", childID, childDone)
	var attemptCtx context.Context
	select {
	case attemptCtx = <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("parent action did not publish its timeout context")
	}
	if !orchestrator.ProgressTimeoutPausedForTest(attemptCtx) {
		t.Fatal("parent timeout controller was not paused during child admission")
	}
	if orchestrator.ExpireProgressTimeoutForTest(attemptCtx) {
		t.Fatal("parent timeout expired while child admission was pending")
	}
	if err := attemptCtx.Err(); err != nil {
		t.Fatalf("parent action ended before explicit cancellation: %v", err)
	}
	select {
	case parent := <-parentDone:
		t.Fatalf("parent finished before explicit cancellation: status=%q err=%v", parent.Status, parent.Error)
	default:
	}
	cancel()

	select {
	case parent := <-parentDone:
		if parent.Status != "failed" {
			t.Fatalf("parent status = %q, want failed after cancellation", parent.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for parent cancellation")
	}

	if err := releaseHolder(); err != nil {
		t.Fatalf("release external holder: %v", err)
	}
	select {
	case <-childDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child after releasing queued child")
	}
}

func TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget(t *testing.T) {
	resetCacheCounter()
	stepGate := installCacheStepGate(t)
	gate := &queuedAwaitParentGate{started: make(chan context.Context, 1), proceed: make(chan struct{})}
	queuedAwaitParentAttempt.Store(gate)
	t.Cleanup(func() { queuedAwaitParentAttempt.CompareAndSwap(gate, nil) })
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "g:plan-level-queued-await-remaining-budget-key",
		HolderID: "external-remaining-budget-holder/-",
		RunID:    "external-remaining-budget-holder",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("external acquire: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("external acquire = %s, want granted", resp.Kind)
	}

	parentDone := make(chan *orchestrator.Result, 1)
	parentFinished := make(chan struct{})
	childDone := make(chan *orchestrator.Result, 1)
	childFinished := make(chan struct{})
	var childStarted atomic.Bool
	var releaseOnce sync.Once
	var releaseErr error
	releaseHolder := func() error {
		releaseOnce.Do(func() {
			_, _, _, releaseErr = st.ReleaseAndNotify(context.Background(),
				"g:plan-level-queued-await-remaining-budget-key", "external-remaining-budget-holder/-", "success", "", "", 0, store.DefaultConcurrencyLease)
		})
		return releaseErr
	}
	t.Cleanup(func() {
		stepGate.letRun()
		cancel()
		if err := releaseHolder(); err != nil {
			t.Errorf("cleanup external holder: %v", err)
		}
		joinCacheDispatchWorker(t, "remaining-budget parent", parentFinished)
		if childStarted.Load() {
			joinCacheDispatchWorker(t, "remaining-budget child", childFinished)
		}
	})
	go func() {
		defer close(parentFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline: "plan-level-queued-await-remaining-budget-parent",
			RunID:    "queued-await-remaining-budget-parent",
		})
		parentDone <- res
	}()
	var attemptCtx context.Context
	select {
	case attemptCtx = <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("parent action did not publish its timeout context")
	}
	const controlledRemainder = 5 * time.Second
	if !orchestrator.SetNodeTimeoutRemainingForTest(attemptCtx, controlledRemainder) {
		t.Fatal("could not establish the parent timeout remainder")
	}
	gate.release()

	childID := waitForSpawnedChildTrigger(t, ctx, st, "queued-await-remaining-budget-parent", "spawn", "plan-level-queued-await-remaining-budget-child")
	claimManualChildTrigger(t, ctx, st, childID)
	childStarted.Store(true)
	go func() {
		defer close(childFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline:    "plan-level-queued-await-remaining-budget-child",
			RunID:       childID,
			ParentRunID: "queued-await-remaining-budget-parent",
		})
		childDone <- res
	}()
	waitForPlanAdmissionWaiter(t, ctx, st, "g:plan-level-queued-await-remaining-budget-key", childID, childDone)
	waitForNodeTimeoutPaused(t, attemptCtx)
	if !orchestrator.NodeTimeoutPausedForTest(attemptCtx) {
		t.Fatal("parent node timeout was not paused during child admission")
	}
	if orchestrator.ForceNodeTimeoutForTest(attemptCtx) {
		t.Fatal("parent node timeout fired while child admission was pending")
	}
	pausedRemaining, paused, ok := orchestrator.NodeTimeoutStateForTest(attemptCtx)
	if !ok || !paused || pausedRemaining <= 0 || pausedRemaining > controlledRemainder {
		t.Fatalf("paused timeout state = remaining %s, paused=%t, ok=%t; want positive remainder no greater than %s", pausedRemaining, paused, ok, controlledRemainder)
	}
	if err := attemptCtx.Err(); err != nil {
		t.Fatalf("parent action ended before child admission: %v", err)
	}

	select {
	case parent := <-parentDone:
		t.Fatalf("parent finished while child was queued for plan admission: status=%q err=%v", parent.Status, parent.Error)
	default:
	}

	if err := releaseHolder(); err != nil {
		t.Fatalf("release external holder: %v", err)
	}
	select {
	case <-stepGate.started:
	case <-ctx.Done():
		t.Fatal("remaining-budget child body did not start")
	}
	resumedRemaining := waitForNodeTimeoutResumed(t, attemptCtx)
	if resumedRemaining > pausedRemaining {
		t.Fatalf("resumed timeout remainder = %s, want no more than paused remainder %s", resumedRemaining, pausedRemaining)
	}
	if !orchestrator.ForceNodeTimeoutForTest(attemptCtx) {
		t.Fatal("resumed parent node timeout could not be forced")
	}
	stepGate.letRun()

	select {
	case parent := <-parentDone:
		if parent.Status != "failed" {
			t.Fatalf("parent status = %q, want failed after remaining timeout budget is spent", parent.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for parent after releasing queued child; parent timeout may still be suppressed")
	}
	parentNodes, err := st.ListNodes(ctx, "queued-await-remaining-budget-parent")
	if err != nil {
		t.Fatalf("list parent nodes: %v", err)
	}
	if len(parentNodes) != 1 || parentNodes[0].FailureReason != store.FailureTimeout {
		t.Fatalf("parent nodes = %+v, want failure reason %q", parentNodes, store.FailureTimeout)
	}
	select {
	case child := <-childDone:
		if child.Status != "success" {
			t.Fatalf("child status = %q, want success (err=%v)", child.Status, child.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child after releasing queued child")
	}
}

func TestConcurrency_RunAndAwaitParentTimeoutPausesBeforeDeadline(t *testing.T) {
	resetCacheCounter()
	stepGate := installCacheStepGate(t)
	gate := &queuedAwaitParentGate{started: make(chan context.Context, 1), proceed: make(chan struct{})}
	queuedAwaitParentAttempt.Store(gate)
	t.Cleanup(func() { queuedAwaitParentAttempt.CompareAndSwap(gate, nil) })
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "g:plan-level-queued-await-early-resume-key",
		HolderID: "external-early-resume-holder/-",
		RunID:    "external-early-resume-holder",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("external acquire: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("external acquire = %s, want granted", resp.Kind)
	}

	parentDone := make(chan *orchestrator.Result, 1)
	parentFinished := make(chan struct{})
	childDone := make(chan *orchestrator.Result, 1)
	childFinished := make(chan struct{})
	var childStarted atomic.Bool
	var releaseOnce sync.Once
	var releaseErr error
	releaseHolder := func() error {
		releaseOnce.Do(func() {
			_, _, _, releaseErr = st.ReleaseAndNotify(context.Background(),
				"g:plan-level-queued-await-early-resume-key", "external-early-resume-holder/-", "success", "", "", 0, store.DefaultConcurrencyLease)
		})
		return releaseErr
	}
	t.Cleanup(func() {
		stepGate.letRun()
		gate.release()
		cancel()
		if err := releaseHolder(); err != nil {
			t.Errorf("cleanup external holder: %v", err)
		}
		joinCacheDispatchWorker(t, "early-resume parent", parentFinished)
		if childStarted.Load() {
			joinCacheDispatchWorker(t, "early-resume child", childFinished)
		}
	})
	go func() {
		defer close(parentFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline: "plan-level-queued-await-early-resume-parent",
			RunID:    "queued-await-early-resume-parent",
		})
		parentDone <- res
	}()
	var attemptCtx context.Context
	select {
	case attemptCtx = <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("parent action did not publish its timeout context")
	}
	const controlledRemainder = time.Second
	if !orchestrator.SetNodeTimeoutRemainingForTest(attemptCtx, controlledRemainder) {
		t.Fatal("could not establish the parent timeout remainder")
	}
	gate.release()

	childID := waitForSpawnedChildTrigger(t, ctx, st, "queued-await-early-resume-parent", "spawn", "plan-level-queued-await-early-resume-child")
	claimManualChildTrigger(t, ctx, st, childID)
	childStarted.Store(true)
	go func() {
		defer close(childFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline:    "plan-level-queued-await-early-resume-child",
			RunID:       childID,
			ParentRunID: "queued-await-early-resume-parent",
		})
		childDone <- res
	}()
	waitForPlanAdmissionWaiter(t, ctx, st, "g:plan-level-queued-await-early-resume-key", childID, childDone)
	waitForNodeTimeoutPaused(t, attemptCtx)
	if !orchestrator.NodeTimeoutPausedForTest(attemptCtx) {
		t.Fatal("parent node timeout was not paused during child admission")
	}
	if orchestrator.ForceNodeTimeoutForTest(attemptCtx) {
		t.Fatal("parent node timeout fired while child admission was pending")
	}
	pausedRemaining, paused, ok := orchestrator.NodeTimeoutStateForTest(attemptCtx)
	if !ok || !paused || pausedRemaining <= 0 || pausedRemaining > controlledRemainder {
		t.Fatalf("paused timeout state = remaining %s, paused=%t, ok=%t; want positive remainder no greater than %s", pausedRemaining, paused, ok, controlledRemainder)
	}
	if err := releaseHolder(); err != nil {
		t.Fatalf("release external holder: %v", err)
	}
	select {
	case <-stepGate.started:
	case <-ctx.Done():
		t.Fatal("early-resume child body did not start")
	}
	resumedRemaining := waitForNodeTimeoutResumed(t, attemptCtx)
	if resumedRemaining > pausedRemaining {
		t.Fatalf("resumed timeout remainder = %s, want no more than paused remainder %s", resumedRemaining, pausedRemaining)
	}
	stepGate.letRun()

	select {
	case parent := <-parentDone:
		if parent.Status != "success" {
			t.Fatalf("parent status = %q, want success after pre-deadline admission wait (err=%v)", parent.Status, parent.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for parent after releasing queued child")
	}
	select {
	case child := <-childDone:
		if child.Status != "success" {
			t.Fatalf("child status = %q, want success (err=%v)", child.Status, child.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child after releasing queued child")
	}
}

type missedPromotionBackend struct {
	orchestrator.ConcurrencyBackend
	key   string
	runID string
	ready chan struct{}
	once  sync.Once
	count atomic.Int32
}

func (b *missedPromotionBackend) ResolveWaiter(
	ctx context.Context,
	key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string,
	bypassRead bool,
) (store.WaiterResolution, error) {
	resolution, err := b.ConcurrencyBackend.ResolveWaiter(ctx, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID, bypassRead)
	if err == nil && key == b.key && runID == b.runID && resolution.Status == store.WaiterStillWaiting && b.count.Add(1) == 3 {
		b.once.Do(func() { close(b.ready) })
	}
	return resolution, err
}

func waitForMissedPromotionChecks(t *testing.T, backend *missedPromotionBackend) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-backend.ready:
	case <-timer.C:
		t.Fatalf("observed %d waiter resolutions, want at least 3", backend.count.Load())
	}
}

func TestConcurrency_RunAndAwaitParentTimeoutCountsMissedPromotionAsAdmissionWait(t *testing.T) {
	resetCacheCounter()
	stepGate := installCacheStepGate(t)
	gate := &queuedAwaitParentGate{started: make(chan context.Context, 1), proceed: make(chan struct{})}
	queuedAwaitParentAttempt.Store(gate)
	t.Cleanup(func() { queuedAwaitParentAttempt.CompareAndSwap(gate, nil) })
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	backends := orchestrator.LocalBackends(p, st, nil)
	observed := &missedPromotionBackend{
		ConcurrencyBackend: backends.Concurrency,
		key:                "g:plan-level-queued-await-missed-promotion-key",
		ready:              make(chan struct{}),
	}
	backends.Concurrency = observed

	resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      "g:plan-level-queued-await-missed-promotion-key",
		HolderID: "external-missed-promotion-holder/-",
		RunID:    "external-missed-promotion-holder",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("external acquire: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("external acquire = %s, want granted", resp.Kind)
	}

	parentDone := make(chan *orchestrator.Result, 1)
	parentFinished := make(chan struct{})
	childDone := make(chan *orchestrator.Result, 1)
	childFinished := make(chan struct{})
	var childStarted atomic.Bool
	var releaseOnce sync.Once
	var releaseErr error
	releaseHolder := func() error {
		releaseOnce.Do(func() {
			_, _, _, releaseErr = st.ReleaseAndNotify(context.Background(),
				"g:plan-level-queued-await-missed-promotion-key", "external-missed-promotion-holder/-", "success", "", "", 0, store.DefaultConcurrencyLease)
		})
		return releaseErr
	}
	t.Cleanup(func() {
		stepGate.letRun()
		gate.release()
		cancel()
		if err := releaseHolder(); err != nil {
			t.Errorf("cleanup external holder: %v", err)
		}
		joinCacheDispatchWorker(t, "missed-promotion parent", parentFinished)
		if childStarted.Load() {
			joinCacheDispatchWorker(t, "missed-promotion child", childFinished)
		}
	})
	go func() {
		defer close(parentFinished)
		res, _ := orchestrator.Run(ctx, backends, orchestrator.Options{
			Pipeline: "plan-level-queued-await-missed-promotion-parent",
			RunID:    "queued-await-missed-promotion-parent",
		})
		parentDone <- res
	}()
	var attemptCtx context.Context
	select {
	case attemptCtx = <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("parent action did not publish its timeout context")
	}
	const controlledRemainder = 600 * time.Millisecond
	if !orchestrator.SetNodeTimeoutRemainingForTest(attemptCtx, controlledRemainder) {
		t.Fatal("could not establish the parent timeout remainder")
	}
	gate.release()

	childID := waitForSpawnedChildTrigger(t, ctx, st, "queued-await-missed-promotion-parent", "spawn", "plan-level-queued-await-missed-promotion-child")
	observed.runID = childID
	claimManualChildTrigger(t, ctx, st, childID)
	childStarted.Store(true)
	go func() {
		defer close(childFinished)
		res, _ := orchestrator.Run(ctx, backends, orchestrator.Options{
			Pipeline:    "plan-level-queued-await-missed-promotion-child",
			RunID:       childID,
			ParentRunID: "queued-await-missed-promotion-parent",
		})
		childDone <- res
	}()
	waitForPlanAdmissionWaiter(t, ctx, st, "g:plan-level-queued-await-missed-promotion-key", childID, childDone)
	waitForMissedPromotionChecks(t, observed)
	waitForNodeTimeoutPaused(t, attemptCtx)
	if !orchestrator.NodeTimeoutPausedForTest(attemptCtx) || orchestrator.ForceNodeTimeoutForTest(attemptCtx) {
		t.Fatal("parent timeout was not safely paused across missed promotion checks")
	}
	pausedRemaining, paused, ok := orchestrator.NodeTimeoutStateForTest(attemptCtx)
	if !ok || !paused || pausedRemaining <= 0 || pausedRemaining > controlledRemainder {
		t.Fatalf("paused timeout state = remaining %s, paused=%t, ok=%t", pausedRemaining, paused, ok)
	}
	if err := releaseHolder(); err != nil {
		t.Fatalf("release external holder: %v", err)
	}
	select {
	case <-stepGate.started:
	case <-ctx.Done():
		t.Fatal("missed-promotion child body did not start")
	}
	resumedRemaining := waitForNodeTimeoutResumed(t, attemptCtx)
	if resumedRemaining > pausedRemaining {
		t.Fatalf("resumed timeout remainder = %s, want no more than paused remainder %s", resumedRemaining, pausedRemaining)
	}
	stepGate.letRun()

	select {
	case parent := <-parentDone:
		if parent.Status != "success" {
			t.Fatalf("parent status = %q, want success after missed promotion accounting (err=%v)", parent.Status, parent.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for parent after releasing queued child")
	}
	select {
	case child := <-childDone:
		if child.Status != "success" {
			t.Fatalf("child status = %q, want success (err=%v)", child.Status, child.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child after releasing queued child")
	}
}

func TestConcurrency_RunAndAwaitParentTimeoutAggregatesMultiKeyAdmissionWait(t *testing.T) {
	resetCacheCounter()
	stepGate := installCacheStepGate(t)
	gate := &queuedAwaitParentGate{started: make(chan context.Context, 1), proceed: make(chan struct{})}
	queuedAwaitParentAttempt.Store(gate)
	t.Cleanup(func() { queuedAwaitParentAttempt.CompareAndSwap(gate, nil) })
	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	for _, key := range []string{"g:plan-level-queued-await-multi-key-a", "g:plan-level-queued-await-multi-key-b"} {
		resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
			Key:      key,
			HolderID: "external-" + key + "/-",
			RunID:    "external-" + key,
			Capacity: 1,
			Policy:   store.OnLimitQueue,
		})
		if err != nil {
			t.Fatalf("external acquire %s: %v", key, err)
		}
		if resp.Kind != store.AcquireGranted {
			t.Fatalf("external acquire %s = %s, want granted", key, resp.Kind)
		}
	}

	parentDone := make(chan *orchestrator.Result, 1)
	parentFinished := make(chan struct{})
	childDone := make(chan *orchestrator.Result, 1)
	childFinished := make(chan struct{})
	var childStarted atomic.Bool
	keys := []string{"g:plan-level-queued-await-multi-key-a", "g:plan-level-queued-await-multi-key-b"}
	var releaseOnce [2]sync.Once
	var releaseErr [2]error
	releaseHolder := func(index int) error {
		releaseOnce[index].Do(func() {
			key := keys[index]
			_, _, _, releaseErr[index] = st.ReleaseAndNotify(context.Background(),
				key, "external-"+key+"/-", "success", "", "", 0, store.DefaultConcurrencyLease)
		})
		return releaseErr[index]
	}
	t.Cleanup(func() {
		stepGate.letRun()
		gate.release()
		cancel()
		for index, key := range keys {
			if err := releaseHolder(index); err != nil {
				t.Errorf("cleanup external holder %s: %v", key, err)
			}
		}
		joinCacheDispatchWorker(t, "multi-key parent", parentFinished)
		if childStarted.Load() {
			joinCacheDispatchWorker(t, "multi-key child", childFinished)
		}
	})
	go func() {
		defer close(parentFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline: "plan-level-queued-await-multi-key-parent",
			RunID:    "queued-await-multi-key-parent",
		})
		parentDone <- res
	}()
	var attemptCtx context.Context
	select {
	case attemptCtx = <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("parent action did not publish its timeout context")
	}
	const controlledRemainder = 480 * time.Millisecond
	if !orchestrator.SetNodeTimeoutRemainingForTest(attemptCtx, controlledRemainder) {
		t.Fatal("could not establish the parent timeout remainder")
	}
	gate.release()

	childID := waitForSpawnedChildTrigger(t, ctx, st, "queued-await-multi-key-parent", "spawn", "plan-level-queued-await-multi-key-child")
	claimManualChildTrigger(t, ctx, st, childID)
	childStarted.Store(true)
	go func() {
		defer close(childFinished)
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline:    "plan-level-queued-await-multi-key-child",
			RunID:       childID,
			ParentRunID: "queued-await-multi-key-parent",
		})
		childDone <- res
	}()
	keyA := keys[0]
	keyB := keys[1]
	waitForPlanAdmissionWaiter(t, ctx, st, keyA, childID, childDone)
	waitForNodeTimeoutPaused(t, attemptCtx)
	if !orchestrator.NodeTimeoutPausedForTest(attemptCtx) || orchestrator.ForceNodeTimeoutForTest(attemptCtx) {
		t.Fatal("parent timeout was not safely paused for key A admission")
	}
	firstRemaining, firstPaused, firstOK := orchestrator.NodeTimeoutStateForTest(attemptCtx)
	if !firstOK || !firstPaused || firstRemaining <= 0 || firstRemaining > controlledRemainder {
		t.Fatalf("key A timeout state = remaining %s, paused=%t, ok=%t", firstRemaining, firstPaused, firstOK)
	}
	if err := releaseHolder(0); err != nil {
		t.Fatalf("release key A holder: %v", err)
	}
	waitForPlanAdmissionWaiter(t, ctx, st, keyB, childID, childDone)
	waitForNodeTimeoutPaused(t, attemptCtx)
	if !orchestrator.NodeTimeoutPausedForTest(attemptCtx) || orchestrator.ForceNodeTimeoutForTest(attemptCtx) {
		t.Fatal("parent timeout was not safely paused for key B admission")
	}
	secondRemaining, secondPaused, secondOK := orchestrator.NodeTimeoutStateForTest(attemptCtx)
	if !secondOK || !secondPaused || secondRemaining <= 0 || secondRemaining > firstRemaining {
		t.Fatalf("key B timeout state = remaining %s, paused=%t, ok=%t; want no more than key A remainder %s", secondRemaining, secondPaused, secondOK, firstRemaining)
	}
	if err := releaseHolder(1); err != nil {
		t.Fatalf("release key B holder: %v", err)
	}
	select {
	case <-stepGate.started:
	case <-ctx.Done():
		t.Fatal("multi-key child body did not start")
	}
	resumedRemaining := waitForNodeTimeoutResumed(t, attemptCtx)
	if resumedRemaining > secondRemaining {
		t.Fatalf("resumed timeout remainder = %s, want no more than key B remainder %s", resumedRemaining, secondRemaining)
	}
	stepGate.letRun()

	select {
	case parent := <-parentDone:
		if parent.Status != "success" {
			t.Fatalf("parent status = %q, want success after multi-key admission accounting (err=%v)", parent.Status, parent.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for parent after releasing queued child")
	}
	select {
	case child := <-childDone:
		if child.Status != "success" {
			t.Fatalf("child status = %q, want success (err=%v)", child.Status, child.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child after releasing queued child")
	}
}

func TestConcurrency_RunAndAwaitParentTimeoutCountsSlowChildPlanning(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	parentDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline: "plan-level-slow-plan-await-parent",
			RunID:    "slow-plan-await-parent",
		})
		parentDone <- res
	}()

	childID := waitForSpawnedChildTrigger(t, ctx, st, "slow-plan-await-parent", "spawn", "plan-level-slow-plan-await-child")
	claimManualChildTrigger(t, ctx, st, childID)
	childDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, nil), orchestrator.Options{
			Pipeline:    "plan-level-slow-plan-await-child",
			RunID:       childID,
			ParentRunID: "slow-plan-await-parent",
		})
		childDone <- res
	}()

	select {
	case parent := <-parentDone:
		if parent.Status != "failed" {
			t.Fatalf("parent status = %q, want failed while child is still planning", parent.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for parent timeout")
	}
	select {
	case <-childDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child after slow planning")
	}
}

func TestConcurrency_PlanLevelSkipShortCircuits(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)

	leaderDone := make(chan *orchestrator.Result, 1)
	leaderFinished := make(chan struct{})
	go func() {
		defer close(leaderFinished)
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "plan-level-skip-leader"})
		leaderDone <- res
	}()
	t.Cleanup(func() {
		releaseLeaderBarrier()
		select {
		case <-leaderFinished:
		case <-time.After(2 * time.Second):
			t.Error("timed out stopping plan-level skip leader")
		}
	})
	waitForLeaderHolding(t)

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

	if current := cacheCounter.inflight.Load(); current != snapshotBefore {
		t.Fatalf("in-flight steps = %d, want unchanged leader count %d", current, snapshotBefore)
	}
	releaseLeaderBarrier()
	select {
	case leader := <-leaderDone:
		if leader.Status != "success" {
			t.Fatalf("leader status = %q, want success (err=%v)", leader.Status, leader.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for plan-level skip leader")
	}
	if finalCount := cacheCounter.inflight.Load(); finalCount != 0 {
		t.Fatalf("in-flight steps after leader completion = %d, want 0", finalCount)
	}
}

func TestConcurrency_CancelOthersEvictsCooperativeLeader(t *testing.T) {
	testCancelOthersStopsLeader(t, "cache-cancel-others-leader", "cache-cancel-others-follower")
}

func TestConcurrency_ForcedReleaseStopsCancelOthersLeader(t *testing.T) {
	testCancelOthersStopsLeader(t, "cache-forced-release-leader", "cache-forced-release-follower")
}

func testCancelOthersStopsLeader(t *testing.T, leaderPipeline, followerPipeline string) {
	t.Helper()
	resetCacheCounter()
	p := newPaths(t)

	leaderDone := make(chan *orchestrator.Result, 1)
	leaderFinished := make(chan struct{})
	go func() {
		defer close(leaderFinished)
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
			Pipeline: leaderPipeline,
		})
		leaderDone <- res
	}()
	t.Cleanup(func() {
		releaseLeaderBarrier()
		select {
		case <-leaderFinished:
		case <-time.After(8 * time.Second):
			t.Error("timed out stopping cache leader")
		}
	})
	waitForLeaderHolding(t)

	followerStart := time.Now()
	followerRes, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: followerPipeline,
	})
	followerElapsed := time.Since(followerStart)

	if followerRes.Status != "success" {
		t.Fatalf("follower status = %q, want success (evicted leader, took slot)", followerRes.Status)
	}
	if followerElapsed > 5*time.Second {
		t.Fatalf("follower took %s; expected eviction well under 5s", followerElapsed)
	}

	select {
	case leader := <-leaderDone:
		if leader.Status != "cancelled" {
			t.Fatalf("leader status = %q (err=%v), want cancelled after supersession", leader.Status, leader.Error)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cooperative leader kept executing after a CancelOthers supersession")
	}
}
