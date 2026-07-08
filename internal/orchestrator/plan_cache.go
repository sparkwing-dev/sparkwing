package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// planCacheOutcome is the short-circuit state of a plan-level Concurrency
// acquire. Non-zero means dispatch returns without scheduling.
type planCacheOutcome string

const (
	planCacheProceed planCacheOutcome = ""        // slot acquired; proceed as normal
	planCacheSkipped planCacheOutcome = "skip"    // OnLimit:Skip, key was full
	planCacheFailed  planCacheOutcome = "fail"    // OnLimit:Fail, key was full
	planCacheEvicted planCacheOutcome = "evicted" // superseded mid-run
)

var inheritedPlanObserveInterval = store.DefaultConcurrencyHeartbeatInterval

// planConcurrencyName returns the plan's whole-run concurrency group
// name, or "" when none was declared. Used for operator-facing errors.
func planConcurrencyName(plan *sparkwing.Plan) string {
	if g := plan.ConcurrencyGroupRef(); g != nil {
		return g.Name()
	}
	return ""
}

// acquirePlanSlot handles plan-level Concurrency() coordination. Caller
// invokes release() at plan terminal. release uses a fresh context so
// it survives a cancelled run. The coordination key is scope-qualified
// through the same scopedGroupKey the node-level path uses, so a
// ScopeBox group and a global group sharing a name never alias, and a
// plan group and a node group with the same name and scope share one
// budget.
func acquirePlanSlot(
	ctx context.Context,
	backends Backends,
	runID string,
	plan *sparkwing.Plan,
	inheritedAdmission planAdmission,
	cancelRun context.CancelCauseFunc,
) (release func(outcome string), outcome planCacheOutcome, activeAdmission planAdmission, err error) {
	group := plan.ConcurrencyGroupRef()
	if group == nil {
		return func(string) {}, planCacheProceed, inheritedAdmission, nil
	}
	key := scopedGroupKey(group, runID)
	limit := group.Limit()
	if backends.Concurrency == nil {
		return nil, "", planAdmission{}, fmt.Errorf("plan Concurrency(%q) declared but Backends.Concurrency is nil", group.Name())
	}
	wedgeBudget, err := storeWedgeBudget()
	if err != nil {
		return nil, "", planAdmission{}, err
	}

	inheritedAdmission = inheritedAdmission.normalized()
	if len(inheritedAdmission.HolderIDs) > 0 {
		if inheritedAdmission.Key == "" || inheritedAdmission.HolderID == "" {
			return nil, "", planAdmission{}, errors.New("plan Concurrency inherited admission is incomplete")
		}
		if inheritedHolderID, ok := inheritedAdmission.holderFor(key); ok {
			resp, err := backends.Concurrency.AcquireSlot(ctx, store.AcquireSlotRequest{
				Key:               key,
				HolderID:          fmt.Sprintf("%s/-", runID),
				InheritedHolderID: inheritedHolderID,
				RunID:             runID,
				NodeID:            "",
				Capacity:          limit.Capacity,
				Policy:            string(limit.OnLimit),
			})
			if err != nil {
				if errors.Is(err, store.ErrConcurrencySuperseded) {
					return nil, planCacheEvicted, planAdmission{}, nil
				}
				return nil, "", planAdmission{}, fmt.Errorf("plan Concurrency inherited admission: %w", err)
			}
			if resp.Kind != store.AcquireGranted {
				return nil, "", planAdmission{}, fmt.Errorf("plan Concurrency inherited admission got %q from acquire", resp.Kind)
			}
			return makeInheritedPlanSlotRelease(backends, key, resp.HolderID, cancelRun),
				planCacheProceed, inheritedAdmission.with(key, resp.HolderID), nil
		}
	}

	holderID := fmt.Sprintf("%s/-", runID)
	admission := inheritedAdmission.with(key, holderID)
	req := store.AcquireSlotRequest{
		Key:      key,
		HolderID: holderID,
		RunID:    runID,
		NodeID:   "",
		Capacity: limit.Capacity,
		Policy:   string(limit.OnLimit),
	}

	resp, err := backends.Concurrency.AcquireSlot(ctx, req)
	if err != nil {
		return nil, "", planAdmission{}, fmt.Errorf("plan Concurrency acquire(%q): %w", key, err)
	}

	if resp.DriftNote != "" {
		payload, _ := json.Marshal(map[string]any{
			"scope":             "plan",
			"key":               key,
			"previous_capacity": resp.PreviousCapacity,
			"new_capacity":      limit.Capacity,
			"note":              resp.DriftNote,
		})
		_ = backends.State.AppendEvent(ctx, runID, "", "concurrency_drift", payload)
	}

	switch resp.Kind {
	case store.AcquireGranted:
		return makePlanSlotRelease(backends, key, holderID, string(limit.OnLimit), wedgeBudget), planCacheProceed, admission, nil

	case store.AcquireSkipped:
		_ = backends.State.AppendEvent(ctx, runID, "", "plan_skipped_concurrent", nil)
		return nil, planCacheSkipped, planAdmission{}, nil

	case store.AcquireFailed:
		_ = backends.State.AppendEvent(ctx, runID, "", "plan_failed_concurrent", nil)
		return nil, planCacheFailed, planAdmission{}, nil

	case store.AcquireQueued, store.AcquireCancellingOthers:
		payload, _ := json.Marshal(map[string]any{
			"scope": "plan",
			"key":   key,
			"kind":  string(resp.Kind),
		})
		_ = backends.State.AppendEvent(ctx, runID, "", "concurrency_wait", payload)

		queueTimeout := time.Duration(0)
		if resp.Kind == store.AcquireQueued {
			queueTimeout = limit.QueueTimeout
		}
		promoted, err := waitForPlanSlot(ctx, backends, key, group.Name(), runID, holderID, queueTimeout, wedgeBudget)
		if err != nil {
			return nil, "", planAdmission{}, err
		}
		if !promoted {
			return nil, planCacheEvicted, planAdmission{}, nil
		}
		return makePlanSlotRelease(backends, key, holderID, string(limit.OnLimit), wedgeBudget), planCacheProceed, admission, nil

	case store.AcquireCoalesced, store.AcquireCached:
		return nil, "", planAdmission{}, fmt.Errorf("plan Concurrency(%q) unexpectedly got %q from acquire", key, resp.Kind)
	}

	return nil, "", planAdmission{}, fmt.Errorf("plan Concurrency acquire returned unknown kind %q", resp.Kind)
}

func makeInheritedPlanSlotRelease(
	backends Backends,
	key string,
	holderID string,
	cancelRun context.CancelCauseFunc,
) func(outcome string) {
	hbCtx, hbCancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(inheritedPlanObserveInterval)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), store.DefaultConcurrencyHeartbeatTimeout)
				_, superseded, err := backends.Concurrency.HeartbeatSlot(ctx, key, holderID, 0)
				cancel()
				if err != nil {
					err = fmt.Errorf("plan Concurrency inherited admission lost for key %q: %w", key, err)
					slog.Warn("inherited plan concurrency heartbeat failed",
						"key", key, "holder_id", holderID, "err", err)
					cancelRun(err)
					return
				}
				if superseded {
					err := fmt.Errorf("plan Concurrency inherited admission superseded for key %q", key)
					slog.Warn("inherited plan concurrency holder superseded",
						"key", key, "holder_id", holderID)
					cancelRun(err)
					return
				}
			}
		}
	}()
	var once sync.Once
	return func(string) {
		once.Do(func() {
			hbCancel()
			wg.Wait()
		})
	}
}

// waitForPlanSlot polls until promoted or cancelled. Plans never
// inherit output, so only those two outcomes are meaningful. A
// non-zero queueTimeout bounds the wait: once it elapses the parked
// waiter is cancelled and the run fails with a queue_timeout error
// naming the group, the configured timeout, and the current holder;
// zero waits indefinitely. A transient ResolveWaiter error keeps
// polling; the wedge guard turns a continuous failure streak past
// wedgeBudget (or one "locking protocol" error) into a terminal error
// instead of a poll loop spinning against a wedged store.
func waitForPlanSlot(ctx context.Context, backends Backends, key, groupName, runID, holderID string, queueTimeout, wedgeBudget time.Duration) (bool, error) {
	wedge := newStoreWedgeGuard(wedgeBudget)
	var deadline time.Time
	if queueTimeout > 0 {
		deadline = time.Now().Add(queueTimeout)
	}
	var lastHolders []store.ConcurrencyHolder
	const pollInterval = 100 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
		res, err := backends.Concurrency.ResolveWaiter(ctx, key, runID, "", "", "", "", false)
		if err != nil {
			if terminal := wedge.fail(fmt.Sprintf("plan concurrency group %q: resolve waiter", groupName), err); terminal != nil {
				return false, terminal
			}
			continue
		}
		wedge.success()
		switch res.Status {
		case store.WaiterStillWaiting:
			if len(res.Holders) > 0 {
				lastHolders = res.Holders
			}
			if !deadline.IsZero() && time.Now().After(deadline) {
				if _, cerr := backends.Concurrency.CancelWaiter(ctx, key, runID, ""); cerr != nil {
					slog.Warn("cancel plan waiter after queue timeout failed; reaper will sweep it",
						"key", key, "run", runID, "err", cerr)
				}
				payload, _ := json.Marshal(map[string]any{
					"scope": "plan", "key": key, "queue_timeout": queueTimeout.String(),
				})
				_ = backends.State.AppendEvent(ctx, runID, "", "concurrency_queue_timeout", payload)
				return false, fmt.Errorf("plan concurrency group %q: queued %s without a slot under OnLimit:Queue (held by %s); a wedged holder shows in `sparkwing box-slots list`", groupName, queueTimeout, heldByLabel(lastHolders))
			}
			continue
		case store.WaiterPromoted:
			return true, nil
		case store.WaiterCancelled:
			return false, nil
		case store.WaiterCached, store.WaiterLeaderFinished:
			return false, fmt.Errorf("plan waiter got unexpected status %q", res.Status)
		}
	}
}

// makePlanSlotRelease builds an idempotent release closure backed by
// a lease-refreshing heartbeat. On contact loss beyond the lease, we
// log loudly but do NOT preempt running nodes (operator chose plan-
// scope coordination, not best-effort). A wedged store stops the
// heartbeat loop -- a "locking protocol" error or a failure streak
// past wedgeBudget -- instead of re-issuing statements forever; the
// lease then lapses and the controller reaps the slot.
func makePlanSlotRelease(backends Backends, key, holderID, onLimit string, wedgeBudget time.Duration) func(outcome string) {
	hbCtx, hbCancel := context.WithCancel(context.Background())
	var superseded atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		wedge := newStoreWedgeGuard(wedgeBudget)
		lease := store.DefaultConcurrencyLease
		t := time.NewTicker(store.ConcurrencyHeartbeatInterval(onLimit))
		defer t.Stop()
		lastOK := time.Now()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), store.ConcurrencyHeartbeatTimeout(onLimit))
				_, was, err := backends.Concurrency.HeartbeatSlot(ctx, key, holderID, lease)
				cancel()
				if err != nil {
					sinceOK := time.Since(lastOK)
					// safety: ErrLockHeld is the store answering fine -- the
					// lease lapsed and another holder owns the slot -- so it
					// feeds the lease-lost branch, never the wedge guard,
					// keeping the "store wedged" telemetry honest.
					if errors.Is(err, store.ErrLockHeld) {
						wedge.success()
						slog.Error("plan concurrency lease lost; slot held by another holder",
							"key", key, "since_last_ok", sinceOK.Round(time.Second))
						continue
					}
					if terminal := wedge.fail(fmt.Sprintf("plan concurrency namespace %q: heartbeat", key), err); terminal != nil {
						slog.Error("plan concurrency heartbeat stopping; store wedged",
							"key", key, "err", terminal)
						return
					}
					slog.Warn("plan concurrency heartbeat failed",
						"key", key, "since_last_ok", sinceOK.Round(time.Second), "err", err)
					if sinceOK >= lease {
						slog.Error("plan concurrency contact lost beyond lease",
							"key", key, "since_last_ok", sinceOK.Round(time.Second),
							"lease", lease)
					}
					continue
				}
				wedge.success()
				lastOK = time.Now()
				if was {
					superseded.Store(true)
				}
			}
		}
	}()

	var once sync.Once
	return func(outcome string) {
		once.Do(func() {
			hbCancel()
			wg.Wait()
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := backends.Concurrency.ReleaseSlot(bg, key, holderID, outcome, "", "", 0); err != nil {
				slog.Warn("plan concurrency release failed; relying on reaper",
					"key", key, "holder_id", holderID, "err", err)
			}
		})
	}
}
