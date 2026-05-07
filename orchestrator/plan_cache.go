package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// planCacheOutcome is the short-circuit state of a plan-level Cache
// acquire. Non-zero means dispatch returns without scheduling.
type planCacheOutcome string

const (
	planCacheProceed planCacheOutcome = ""        // slot acquired; proceed as normal
	planCacheSkipped planCacheOutcome = "skip"    // OnLimit:Skip, key was full
	planCacheFailed  planCacheOutcome = "fail"    // OnLimit:Fail, key was full
	planCacheEvicted planCacheOutcome = "evicted" // superseded mid-run
)

// acquirePlanSlot handles plan-level .Cache() coordination. Caller
// invokes release() at plan terminal. release uses a fresh context so
// it survives a cancelled run.
func acquirePlanSlot(ctx context.Context, backends Backends, runID string, plan *sparkwing.Plan) (release func(outcome string), outcome planCacheOutcome, err error) {
	opts := plan.CacheOpts()
	if !opts.HasKey() {
		return func(string) {}, planCacheProceed, nil
	}
	if backends.Concurrency == nil {
		return nil, "", fmt.Errorf("plan Cache(%q) declared but Backends.Concurrency is nil", opts.Key)
	}

	holderID := fmt.Sprintf("%s/-", runID)
	req := store.AcquireSlotRequest{
		Key:           opts.Key,
		HolderID:      holderID,
		RunID:         runID,
		NodeID:        "",
		Capacity:      opts.Max,
		Policy:        string(opts.OnLimit),
		CancelTimeout: opts.CancelTimeout,
	}

	resp, err := backends.Concurrency.AcquireSlot(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("plan Cache acquire(%q): %w", opts.Key, err)
	}

	if resp.DriftNote != "" {
		payload, _ := json.Marshal(map[string]any{
			"scope":             "plan",
			"key":               opts.Key,
			"previous_capacity": resp.PreviousCapacity,
			"new_capacity":      opts.Max,
			"note":              resp.DriftNote,
		})
		_ = backends.State.AppendEvent(ctx, runID, "", "concurrency_drift", payload)
	}

	switch resp.Kind {
	case store.AcquireGranted:
		return makePlanSlotRelease(backends, opts.Key, holderID), planCacheProceed, nil

	case store.AcquireSkipped:
		_ = backends.State.AppendEvent(ctx, runID, "", "plan_skipped_concurrent", nil)
		return nil, planCacheSkipped, nil

	case store.AcquireFailed:
		_ = backends.State.AppendEvent(ctx, runID, "", "plan_failed_concurrent", nil)
		return nil, planCacheFailed, nil

	case store.AcquireQueued, store.AcquireCancellingOthers:
		payload, _ := json.Marshal(map[string]any{
			"scope": "plan",
			"key":   opts.Key,
			"kind":  string(resp.Kind),
		})
		_ = backends.State.AppendEvent(ctx, runID, "", "concurrency_wait", payload)

		// Back-stop: if evicted holders refuse to terminate within
		// CancelTimeout, force-release so progress is bounded.
		if resp.Kind == store.AcquireCancellingOthers && opts.CancelTimeout > 0 {
			timer := time.AfterFunc(opts.CancelTimeout, func() {
				bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = backends.Concurrency.ForceReleaseSuperseded(bg, opts.Key)
			})
			defer timer.Stop()
		}

		promoted, err := waitForPlanSlot(ctx, backends, opts.Key, runID, holderID)
		if err != nil {
			return nil, "", err
		}
		if !promoted {
			return nil, planCacheEvicted, nil
		}
		return makePlanSlotRelease(backends, opts.Key, holderID), planCacheProceed, nil

	case store.AcquireCoalesced, store.AcquireCached:
		// Coalesce + CacheKey are rejected at plan build.
		return nil, "", fmt.Errorf("plan Cache(%q) unexpectedly got %q from acquire; this should have been rejected at build", opts.Key, resp.Kind)
	}

	return nil, "", fmt.Errorf("plan Cache acquire returned unknown kind %q", resp.Kind)
}

// waitForPlanSlot polls until promoted or cancelled. Plans never
// inherit output, so only those two outcomes are meaningful.
func waitForPlanSlot(ctx context.Context, backends Backends, key, runID, holderID string) (bool, error) {
	const pollInterval = 100 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
		res, err := backends.Concurrency.ResolveWaiter(ctx, key, runID, "", "", "", "")
		if err != nil {
			return false, err
		}
		switch res.Status {
		case store.WaiterStillWaiting:
			continue
		case store.WaiterPromoted:
			return true, nil
		case store.WaiterCancelled:
			return false, nil
		case store.WaiterCached, store.WaiterLeaderFinished:
			// Node-level only; unexpected at plan scope.
			return false, fmt.Errorf("plan waiter got unexpected status %q", res.Status)
		}
	}
}

// makePlanSlotRelease builds an idempotent release closure backed by
// a lease-refreshing heartbeat. On contact loss beyond the lease, we
// log loudly but do NOT preempt running nodes (operator chose plan-
// scope coordination, not best-effort).
func makePlanSlotRelease(backends Backends, key, holderID string) func(outcome string) {
	hbCtx, hbCancel := context.WithCancel(context.Background())
	var superseded atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lease := store.DefaultConcurrencyLease
		t := time.NewTicker(store.DefaultConcurrencyHeartbeatInterval)
		defer t.Stop()
		lastOK := time.Now()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), store.DefaultConcurrencyHeartbeatTimeout)
				_, was, err := backends.Concurrency.HeartbeatSlot(ctx, key, holderID, lease)
				cancel()
				if err != nil {
					sinceOK := time.Since(lastOK)
					slog.Warn("plan concurrency heartbeat failed",
						"key", key, "since_last_ok", sinceOK.Round(time.Second), "err", err)
					if sinceOK >= lease {
						slog.Error("plan concurrency contact lost beyond lease",
							"key", key, "since_last_ok", sinceOK.Round(time.Second),
							"lease", lease)
					}
					continue
				}
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
