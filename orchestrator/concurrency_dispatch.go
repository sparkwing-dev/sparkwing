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

	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// runNodeWithCache owns the full .Cache() lifecycle: acquire, policy
// branching, coalesce/queue wait, execute, release.
func (r *InProcessRunner) runNodeWithCache(ctx context.Context, req runner.Request) (runner.Result, bool) {
	node := req.Node
	opts := node.CacheOpts()
	if !opts.HasKey() {
		return runner.Result{}, false
	}

	logger := slog.Default()
	var cacheHash string
	if opts.CacheKey != nil {
		k := safeCacheKey(ctx, opts.CacheKey, node.ID())
		cacheHash = string(k)
	}

	holderID := fmt.Sprintf("%s/%s", req.RunID, node.ID())
	acquireReq := store.AcquireSlotRequest{
		Key:           opts.Key,
		HolderID:      holderID,
		RunID:         req.RunID,
		NodeID:        node.ID(),
		Capacity:      opts.Max,
		Policy:        string(opts.OnLimit),
		CacheKeyHash:  cacheHash,
		CacheTTL:      opts.CacheTTL,
		CancelTimeout: opts.CancelTimeout,
	}

	resp, err := r.backends.Concurrency.AcquireSlot(ctx, acquireReq)
	if err != nil {
		r.markFailed(ctx, req.RunID, node.ID(), fmt.Errorf("concurrency acquire(%q): %w", opts.Key, err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}, true
	}

	if resp.DriftNote != "" {
		payload, _ := json.Marshal(map[string]any{
			"key":               opts.Key,
			"previous_capacity": resp.PreviousCapacity,
			"new_capacity":      opts.Max,
			"note":              resp.DriftNote,
		})
		_ = r.backends.State.AppendEvent(ctx, req.RunID, node.ID(), "concurrency_drift", payload)
		logger.Warn("concurrency drift", "key", opts.Key, "prev", resp.PreviousCapacity, "new", opts.Max)
	}

	switch resp.Kind {
	case store.AcquireCached:
		return r.applyCacheHit(ctx, req, opts, cacheHash, resp.OutputRef, resp.OriginRunID, resp.OriginNodeID), true

	case store.AcquireSkipped:
		return r.applySkippedConcurrent(ctx, req), true

	case store.AcquireFailed:
		err := fmt.Errorf("concurrency key %q slot full under OnLimit:Fail", opts.Key)
		r.markFailed(ctx, req.RunID, node.ID(), err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}, true

	case store.AcquireGranted:
		return r.runHeldSlot(ctx, req, opts, holderID, cacheHash), true

	case store.AcquireQueued, store.AcquireCoalesced, store.AcquireCancellingOthers:
		return r.waitThenRun(ctx, req, opts, cacheHash, resp), true
	}

	err = fmt.Errorf("concurrency acquire returned unknown kind %q", resp.Kind)
	r.markFailed(ctx, req.RunID, node.ID(), err)
	return runner.Result{Outcome: sparkwing.Failed, Err: err}, true
}

// applyCacheHit stamps a cache-hit outcome and replays the origin's
// output, with node_start/node_end + cache_hit bookkeeping.
func (r *InProcessRunner) applyCacheHit(ctx context.Context, req runner.Request, opts sparkwing.CacheOptions, cacheHash, outputRef, originRun, originNode string) runner.Result {
	output, err := r.fetchCachedOutput(ctx, outputRef, originRun, originNode)
	if err != nil {
		r.markFailed(ctx, req.RunID, req.Node.ID(), fmt.Errorf("cache hit: fetch output: %w", err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}

	_ = r.backends.State.StartNode(ctx, req.RunID, req.Node.ID())
	payload, _ := json.Marshal(map[string]any{
		"key":            opts.Key,
		"cache_key_hash": cacheHash,
		"origin_run_id":  originRun,
		"origin_node_id": originNode,
	})
	_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "cache_hit", payload)
	_ = r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(sparkwing.Cached), "", output)

	if nlog, err := r.backends.Logs.OpenNodeLog(req.RunID, req.Node.ID(), req.Delegate); err == nil {
		nlog = wrapNodeLogWithMasker(nlog, secrets.MaskerFromContext(ctx))
		ts := time.Now()
		nlog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_start", Attrs: map[string]any{"cache_hit": true}})
		nlog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_end", Attrs: map[string]any{
			"outcome": string(sparkwing.Cached), "duration_ms": int64(0), "cache_hit": true,
		}})
		_ = nlog.Close()
	}

	return runner.Result{Outcome: sparkwing.Cached, Output: output}
}

// applySkippedConcurrent resolves a node that arrived at a full slot
// under OnLimit:Skip.
func (r *InProcessRunner) applySkippedConcurrent(ctx context.Context, req runner.Request) runner.Result {
	_ = r.backends.State.StartNode(ctx, req.RunID, req.Node.ID())
	_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "node_skipped_concurrent", nil)
	_ = r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(sparkwing.SkippedConcurrent), "", nil)

	if nlog, err := r.backends.Logs.OpenNodeLog(req.RunID, req.Node.ID(), req.Delegate); err == nil {
		nlog = wrapNodeLogWithMasker(nlog, secrets.MaskerFromContext(ctx))
		ts := time.Now()
		nlog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_start"})
		nlog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_end", Attrs: map[string]any{
			"outcome": string(sparkwing.SkippedConcurrent), "duration_ms": int64(0),
		}})
		_ = nlog.Close()
	}
	return runner.Result{Outcome: sparkwing.SkippedConcurrent}
}

// runHeldSlot executes the node while a heartbeat extends the lease
// and watches for supersede; on supersede execCtx cancels and the
// node finalizes as superseded.
func (r *InProcessRunner) runHeldSlot(ctx context.Context, req runner.Request, opts sparkwing.CacheOptions, holderID, cacheHash string) runner.Result {
	execCtx, cancelExec := context.WithCancel(ctx)
	var superseded atomic.Bool
	stopHB := r.startSlotHeartbeat(execCtx, opts.Key, holderID, &superseded, cancelExec)

	defer func() {
		stopHB()
		cancelExec()
		ctxBG, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		outcome := r.lastReleaseOutcomeFor(req.RunID, req.Node.ID())
		outputRef := fmt.Sprintf("%s/%s", req.RunID, req.Node.ID())
		if err := r.backends.Concurrency.ReleaseSlot(ctxBG, opts.Key, holderID, outcome, outputRef, cacheHash, opts.CacheTTL); err != nil {
			slog.Warn("concurrency release failed; relying on reaper",
				"key", opts.Key, "holder_id", holderID, "err", err)
		}
	}()

	// SkipIf evaluated after acquisition so the predicate sees the
	// executing-node env. Skipped still releases via defer.
	if reason, skip := evalSkipPredicates(execCtx, req.Node); skip {
		r.markSkipped(execCtx, req.RunID, req.Node.ID(), reason)
		r.recordReleaseOutcome(req.RunID, req.Node.ID(), string(sparkwing.Skipped))
		return runner.Result{Outcome: sparkwing.Skipped}
	}

	output, err := r.executeNode(execCtx, req.RunID, req.Node, req.Delegate)
	if superseded.Load() {
		err := fmt.Errorf("concurrency key %q: holder superseded by newer arrival", opts.Key)
		_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "node_superseded", []byte(err.Error()))
		_ = r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(sparkwing.Superseded), err.Error(), nil)
		r.recordReleaseOutcome(req.RunID, req.Node.ID(), string(sparkwing.Superseded))
		return runner.Result{Outcome: sparkwing.Superseded, Err: err}
	}
	if err != nil {
		r.recordReleaseOutcome(req.RunID, req.Node.ID(), string(sparkwing.Failed))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	r.recordReleaseOutcome(req.RunID, req.Node.ID(), string(sparkwing.Success))
	return runner.Result{Outcome: sparkwing.Success, Output: output}
}

// startSlotHeartbeat extends the slot lease and watches for supersede.
// Fail-closed: if no successful heartbeat in `lease`, the controller
// has reaped us; we abort so a newer holder isn't racing the same
// work. The returned stop is safe to call multiple times.
func (r *InProcessRunner) startSlotHeartbeat(ctx context.Context, key, holderID string, superseded *atomic.Bool, cancelExec context.CancelFunc) func() {
	done := make(chan struct{})
	var once sync.Once

	lease := store.DefaultConcurrencyLease

	go func() {
		t := time.NewTicker(store.DefaultConcurrencyHeartbeatInterval)
		defer t.Stop()
		lastOK := time.Now()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				hbCtx, cancel := context.WithTimeout(context.Background(), store.DefaultConcurrencyHeartbeatTimeout)
				_, wasSuperseded, err := r.backends.Concurrency.HeartbeatSlot(hbCtx, key, holderID, lease)
				cancel()
				if err != nil {
					sinceOK := time.Since(lastOK)
					slog.Warn("concurrency heartbeat failed",
						"key", key, "holder", holderID,
						"since_last_ok", sinceOK.Round(time.Second),
						"err", err)
					if sinceOK >= lease {
						slog.Error("concurrency contact lost beyond lease; aborting work",
							"key", key, "holder", holderID,
							"since_last_ok", sinceOK.Round(time.Second),
							"lease", lease)
						superseded.Store(true)
						cancelExec()
						return
					}
					continue
				}
				lastOK = time.Now()
				if wasSuperseded {
					superseded.Store(true)
					cancelExec()
					return
				}
			}
		}
	}()

	return func() { once.Do(func() { close(done) }) }
}

// waitThenRun polls ResolveWaiter and transitions on first resolution.
func (r *InProcessRunner) waitThenRun(ctx context.Context, req runner.Request, opts sparkwing.CacheOptions, cacheHash string, initial store.AcquireSlotResponse) runner.Result {
	leaderRun, leaderNode := initial.LeaderRunID, initial.LeaderNodeID

	payload, _ := json.Marshal(map[string]any{
		"key":            opts.Key,
		"kind":           string(initial.Kind),
		"leader_run_id":  leaderRun,
		"leader_node_id": leaderNode,
	})
	_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "concurrency_wait", payload)

	// Back-stop: force-release evicted holders after CancelTimeout so
	// forward progress is bounded.
	if initial.Kind == store.AcquireCancellingOthers && opts.CancelTimeout > 0 {
		timer := time.AfterFunc(opts.CancelTimeout, func() {
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dropped, err := r.backends.Concurrency.ForceReleaseSuperseded(bg, opts.Key)
			if err != nil {
				slog.Warn("force-release after CancelTimeout failed", "key", opts.Key, "err", err)
				return
			}
			if len(dropped) > 0 {
				dropPayload, _ := json.Marshal(map[string]any{
					"key":     opts.Key,
					"count":   len(dropped),
					"reason":  "cancel_timeout",
					"timeout": opts.CancelTimeout.String(),
				})
				_ = r.backends.State.AppendEvent(bg, req.RunID, req.Node.ID(), "concurrency_force_release", dropPayload)
			}
		})
		defer timer.Stop()
	}

	// In-process only (cluster's HTTPConcurrency stubs ResolveWaiter).
	const pollInterval = 100 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_, _ = r.backends.Concurrency.AcquireSlot(context.Background(), store.AcquireSlotRequest{})
			r.markFailed(ctx, req.RunID, req.Node.ID(), ctx.Err())
			return runner.Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
		case <-ticker.C:
		}

		res, err := r.backends.Concurrency.ResolveWaiter(ctx, opts.Key, req.RunID, req.Node.ID(), cacheHash, leaderRun, leaderNode)
		if err != nil {
			r.markFailed(ctx, req.RunID, req.Node.ID(), fmt.Errorf("resolve waiter: %w", err))
			return runner.Result{Outcome: sparkwing.Failed, Err: err}
		}

		switch res.Status {
		case store.WaiterStillWaiting:
			continue
		case store.WaiterPromoted:
			_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "concurrency_promoted", nil)
			return r.runHeldSlot(ctx, req, opts, res.HolderID, cacheHash)
		case store.WaiterCached:
			return r.applyCacheHit(ctx, req, opts, cacheHash, res.OutputRef, res.OriginRunID, res.OriginNodeID)
		case store.WaiterLeaderFinished:
			return r.inheritLeaderOutcome(ctx, req, opts, res.LeaderRunID, res.LeaderNodeID)
		case store.WaiterCancelled:
			err := fmt.Errorf("concurrency key %q: waiter was cancelled or superseded", opts.Key)
			_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "concurrency_cancelled", nil)
			_ = r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(sparkwing.Superseded), err.Error(), nil)
			return runner.Result{Outcome: sparkwing.Superseded, Err: err}
		}
	}
}

// inheritLeaderOutcome adopts the leader's terminal outcome + output
// when it finished without writing a cache entry. Failed leaders
// produce failed followers.
func (r *InProcessRunner) inheritLeaderOutcome(ctx context.Context, req runner.Request, opts sparkwing.CacheOptions, leaderRunID, leaderNodeID string) runner.Result {
	output, err := r.backends.State.GetNodeOutput(ctx, leaderRunID, leaderNodeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		r.markFailed(ctx, req.RunID, req.Node.ID(), fmt.Errorf("fetch leader output: %w", err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}

	_ = r.backends.State.StartNode(ctx, req.RunID, req.Node.ID())
	payload, _ := json.Marshal(map[string]any{
		"key":            opts.Key,
		"leader_run_id":  leaderRunID,
		"leader_node_id": leaderNodeID,
	})
	_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "coalesced", payload)

	outcome := sparkwing.Success
	if leader, err := r.backends.State.GetRun(ctx, leaderRunID); err == nil && leader != nil && leader.Status == "failed" {
		outcome = sparkwing.Failed
	}
	_ = r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(outcome), "", output)

	if nlog, err := r.backends.Logs.OpenNodeLog(req.RunID, req.Node.ID(), req.Delegate); err == nil {
		nlog = wrapNodeLogWithMasker(nlog, secrets.MaskerFromContext(ctx))
		ts := time.Now()
		nlog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_start", Attrs: map[string]any{
			"coalesced_from": fmt.Sprintf("%s/%s", leaderRunID, leaderNodeID),
		}})
		nlog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_end", Attrs: map[string]any{
			"outcome": string(outcome), "duration_ms": int64(0),
			"coalesced_from": fmt.Sprintf("%s/%s", leaderRunID, leaderNodeID),
		}})
		_ = nlog.Close()
	}
	return runner.Result{Outcome: outcome, Output: output}
}

// fetchCachedOutput resolves output_ref to the origin's stored bytes.
func (r *InProcessRunner) fetchCachedOutput(ctx context.Context, outputRef, originRun, originNode string) ([]byte, error) {
	_ = outputRef // reserved for future encodings
	return r.backends.State.GetNodeOutput(ctx, originRun, originNode)
}

// In-memory sidechannel so runHeldSlot's defer learns the outcome.
// Lost on crash; reaper handles orphan holders.
var inflightOutcomes = &inflightMap{m: map[string]string{}}

type inflightMap struct {
	mu sync.Mutex
	m  map[string]string
}

func (i *inflightMap) set(runID, nodeID, outcome string) {
	i.mu.Lock()
	i.m[runID+"/"+nodeID] = outcome
	i.mu.Unlock()
}

func (i *inflightMap) get(runID, nodeID string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	outcome, ok := i.m[runID+"/"+nodeID]
	if !ok {
		return "success"
	}
	delete(i.m, runID+"/"+nodeID)
	return outcome
}

func (r *InProcessRunner) recordReleaseOutcome(runID, nodeID, outcome string) {
	inflightOutcomes.set(runID, nodeID, outcome)
}

func (r *InProcessRunner) lastReleaseOutcomeFor(runID, nodeID string) string {
	return inflightOutcomes.get(runID, nodeID)
}
