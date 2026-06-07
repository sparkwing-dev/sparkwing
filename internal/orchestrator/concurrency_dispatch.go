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

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// concWaitDetail renders a short status_detail string describing why a
// node is waiting on a concurrency namespace, for the dashboard. Empty
// for kinds that don't represent a wait.
func concWaitDetail(namespace string, r store.AcquireSlotResponse, leaderRun, leaderNode string) string {
	switch r.Kind {
	case store.AcquireQueued:
		return concQueuedDetail(namespace, r.Position, r.Holders)
	case store.AcquireCoalesced:
		return fmt.Sprintf("coalescing in %s behind %s", namespace, holderLabel(leaderRun, leaderNode))
	case store.AcquireCancellingOthers:
		return fmt.Sprintf("waiting in %s (evicting prior holders)", namespace)
	default:
		return ""
	}
}

// concQueuedDetail renders the "queued in <ns>: N ahead, held by X"
// summary for a queue-policy waiter.
func concQueuedDetail(namespace string, position int, holders []store.ConcurrencyHolder) string {
	held := "unknown"
	if len(holders) > 0 {
		held = holderLabel(holders[0].RunID, holders[0].NodeID)
		if extra := len(holders) - 1; extra > 0 {
			held = fmt.Sprintf("%s +%d", held, extra)
		}
	}
	return fmt.Sprintf("queued in %s: %d ahead, held by %s", namespace, position, held)
}

// emitConcWaitLog mirrors a concurrency-wait line into the node log and,
// via the run delegate, the live stream. The node log is append-mode, so
// executeNode's later open on promotion appends cleanly.
func (r *InProcessRunner) emitConcWaitLog(ctx context.Context, req runner.Request, detail string) {
	if nlog, err := r.backends.Logs.OpenNodeLog(req.RunID, req.Node.ID(), req.Delegate); err == nil {
		nlog.Emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Event: "concurrency_wait", Msg: detail})
		_ = nlog.Close()
	}
}

func holderLabel(runID, nodeID string) string {
	if nodeID == "" {
		return runID
	}
	return runID + "/" + nodeID
}

// contentCacheKey is the synthetic coordination namespace a node uses
// when it declares Cache() but no Concurrency() group. Capacity is
// effectively unbounded, so admission always grants and cache entries
// key purely on the content hash.
//
// chunk 2: pure-content caching gets its own store path; until then a
// cache-only node rides the concurrency slot machinery under this
// shared key, and a node that declares both Cache and Concurrency keys
// its memo on the group name rather than on content alone.
const contentCacheKey = "sparkwing/content-cache"

const contentCacheCapacity = 1 << 30

// coordParams is the resolved coordination input for one node, built
// from its Concurrency group and/or Cache config. It replaces the old
// CacheOptions the dispatch path used to read.
type coordParams struct {
	key           string
	capacity      int
	policy        string
	cacheHash     string
	cacheTTL      time.Duration
	cancelTimeout time.Duration
	queueTimeout  time.Duration
}

// runNodeWithCache owns the full Cache()/Concurrency() lifecycle:
// acquire, policy branching, queue wait, execute, release.
func (r *InProcessRunner) runNodeWithCache(ctx context.Context, req runner.Request) (runner.Result, bool) {
	node := req.Node
	group := node.ConcurrencyGroupRef()
	cacheCfg := node.CacheConfig()
	if group == nil && cacheCfg == nil {
		return runner.Result{}, false
	}

	logger := slog.Default()
	cp := coordParams{
		key:      contentCacheKey,
		capacity: contentCacheCapacity,
		policy:   string(sparkwing.Queue),
	}
	if group != nil {
		cp.key = group.Name()
		cp.capacity = group.Limit().Capacity
		cp.policy = string(group.Limit().OnLimit)
	}
	if cacheCfg != nil {
		cp.cacheTTL = cacheCfg.TTL
		k := safeCacheKey(ctx, cacheCfg.Key, node.ID())
		switch {
		case k == sparkwing.NoCache:
			sparkwing.LoggerFromContext(ctx).Log("info",
				fmt.Sprintf("Cache(%s) returned NoCache; memoization explicitly skipped", node.ID()))
		case k == "":
			sparkwing.LoggerFromContext(ctx).Log("warn",
				fmt.Sprintf("Cache(%s) returned empty CacheKey; memoization skipped (treating as missing key -- return sparkwing.NoCache to opt out explicitly)", node.ID()))
		default:
			cp.cacheHash = string(k)
		}
	}

	holderID := fmt.Sprintf("%s/%s", req.RunID, node.ID())
	acquireReq := store.AcquireSlotRequest{
		Key:           cp.key,
		HolderID:      holderID,
		RunID:         req.RunID,
		NodeID:        node.ID(),
		Capacity:      cp.capacity,
		Policy:        cp.policy,
		CacheKeyHash:  cp.cacheHash,
		CacheTTL:      cp.cacheTTL,
		CancelTimeout: cp.cancelTimeout,
		BypassRead:    noCacheFromContext(ctx),
	}

	resp, err := r.backends.Concurrency.AcquireSlot(ctx, acquireReq)
	if err != nil {
		r.markFailed(ctx, req.RunID, node.ID(), fmt.Errorf("concurrency acquire(%q): %w", cp.key, err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}, true
	}

	if resp.DriftNote != "" {
		payload, _ := json.Marshal(map[string]any{
			"key":               cp.key,
			"previous_capacity": resp.PreviousCapacity,
			"new_capacity":      cp.capacity,
			"note":              resp.DriftNote,
		})
		_ = r.backends.State.AppendEvent(ctx, req.RunID, node.ID(), "concurrency_drift", payload)
		logger.Warn("concurrency drift", "key", cp.key, "prev", resp.PreviousCapacity, "new", cp.capacity)
	}

	switch resp.Kind {
	case store.AcquireCached:
		return r.applyCacheHit(ctx, req, cp, resp.OutputRef, resp.OriginRunID, resp.OriginNodeID), true

	case store.AcquireSkipped:
		return r.applySkippedConcurrent(ctx, req), true

	case store.AcquireFailed:
		err := fmt.Errorf("concurrency key %q slot full under OnLimit:Fail", cp.key)
		r.markFailed(ctx, req.RunID, node.ID(), err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}, true

	case store.AcquireGranted:
		return r.runHeldSlot(ctx, req, cp, holderID), true

	case store.AcquireQueued, store.AcquireCoalesced, store.AcquireCancellingOthers:
		return r.waitThenRun(ctx, req, cp, resp), true
	}

	err = fmt.Errorf("concurrency acquire returned unknown kind %q", resp.Kind)
	r.markFailed(ctx, req.RunID, node.ID(), err)
	return runner.Result{Outcome: sparkwing.Failed, Err: err}, true
}

// applyCacheHit stamps a cache-hit outcome and replays the origin's
// output, with node_start/node_end + cache_hit bookkeeping.
func (r *InProcessRunner) applyCacheHit(ctx context.Context, req runner.Request, cp coordParams, outputRef, originRun, originNode string) runner.Result {
	output, err := r.fetchCachedOutput(ctx, outputRef, originRun, originNode)
	if err != nil {
		r.markFailed(ctx, req.RunID, req.Node.ID(), fmt.Errorf("cache hit: fetch output: %w", err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}

	_ = r.backends.State.StartNode(ctx, req.RunID, req.Node.ID())
	payload, _ := json.Marshal(map[string]any{
		"key":            cp.key,
		"cache_key_hash": cp.cacheHash,
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
func (r *InProcessRunner) runHeldSlot(ctx context.Context, req runner.Request, cp coordParams, holderID string) runner.Result {
	execCtx, cancelExec := context.WithCancel(ctx)
	var superseded atomic.Bool
	stopHB := r.startSlotHeartbeat(execCtx, cp.key, holderID, &superseded, cancelExec)

	defer func() {
		stopHB()
		cancelExec()
		ctxBG, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		outcome := r.lastReleaseOutcomeFor(req.RunID, req.Node.ID())
		outputRef := fmt.Sprintf("%s/%s", req.RunID, req.Node.ID())
		if err := r.backends.Concurrency.ReleaseSlot(ctxBG, cp.key, holderID, outcome, outputRef, cp.cacheHash, cp.cacheTTL); err != nil {
			slog.Warn("concurrency release failed; relying on reaper",
				"key", cp.key, "holder_id", holderID, "err", err)
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
		err := fmt.Errorf("concurrency key %q: holder superseded by newer arrival", cp.key)
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
func (r *InProcessRunner) waitThenRun(ctx context.Context, req runner.Request, cp coordParams, initial store.AcquireSlotResponse) runner.Result {
	leaderRun, leaderNode := initial.LeaderRunID, initial.LeaderNodeID

	holders := make([]map[string]string, 0, len(initial.Holders))
	for _, h := range initial.Holders {
		holders = append(holders, map[string]string{"run_id": h.RunID, "node_id": h.NodeID})
	}
	payload, _ := json.Marshal(map[string]any{
		"key":            cp.key,
		"kind":           string(initial.Kind),
		"position":       initial.Position,
		"queue_length":   initial.QueueLength,
		"holders":        holders,
		"leader_run_id":  leaderRun,
		"leader_node_id": leaderNode,
	})
	_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "concurrency_wait", payload)

	// Surface the wait on the node so the dashboard shows "queued ... N
	// ahead, held by X" instead of an indistinguishable spinner, and emit
	// it into the log stream (from the dispatcher -- the node hasn't
	// started its runner yet). Refreshed below as the queue advances;
	// cleared on promotion.
	lastDetail := concWaitDetail(cp.key, initial, leaderRun, leaderNode)
	if lastDetail != "" {
		_ = r.backends.State.UpdateNodeActivity(ctx, req.RunID, req.Node.ID(), lastDetail)
		r.emitConcWaitLog(ctx, req, lastDetail)
	}
	// Only queue waiters have an advancing position worth refreshing;
	// coalesce/cancel-others keep their initial detail.
	queueRefresh := initial.Kind == store.AcquireQueued

	// Back-stop: force-release evicted holders after the cancel timeout
	// so forward progress is bounded. cancelTimeout is zero in chunk 1
	// (the new API drops the knob), so this relies on the store's
	// default eviction; the timer below only arms if a timeout is set.
	if initial.Kind == store.AcquireCancellingOthers && cp.cancelTimeout > 0 {
		timer := time.AfterFunc(cp.cancelTimeout, func() {
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dropped, err := r.backends.Concurrency.ForceReleaseSuperseded(bg, cp.key)
			if err != nil {
				slog.Warn("force-release after cancel timeout failed", "key", cp.key, "err", err)
				return
			}
			if len(dropped) > 0 {
				dropPayload, _ := json.Marshal(map[string]any{
					"key":     cp.key,
					"count":   len(dropped),
					"reason":  "cancel_timeout",
					"timeout": cp.cancelTimeout.String(),
				})
				_ = r.backends.State.AppendEvent(bg, req.RunID, req.Node.ID(), "concurrency_force_release", dropPayload)
			}
		})
		defer timer.Stop()
	}

	// queueTimeout bounds a queued waiter's wait. Zero = wait forever
	// (historical behavior, and the chunk-1 default since the new API
	// drops the knob). Only the Queue policy honors it.
	var queueDeadline time.Time
	if cp.queueTimeout > 0 && initial.Kind == store.AcquireQueued {
		queueDeadline = time.Now().Add(cp.queueTimeout)
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

		res, err := r.backends.Concurrency.ResolveWaiter(ctx, cp.key, req.RunID, req.Node.ID(), cp.cacheHash, leaderRun, leaderNode)
		if err != nil {
			r.markFailed(ctx, req.RunID, req.Node.ID(), fmt.Errorf("resolve waiter: %w", err))
			return runner.Result{Outcome: sparkwing.Failed, Err: err}
		}

		switch res.Status {
		case store.WaiterStillWaiting:
			if !queueDeadline.IsZero() && time.Now().After(queueDeadline) {
				return r.failQueueTimeout(ctx, req, cp)
			}
			// Refresh the wait display as the queue advances. ResolveWaiter
			// recomputes position against the fully-committed queue, so this
			// self-corrects any stale insert-time value. Only writes when the
			// summary actually changes.
			if queueRefresh {
				if d := concQueuedDetail(cp.key, res.Position, res.Holders); d != lastDetail {
					lastDetail = d
					_ = r.backends.State.UpdateNodeActivity(ctx, req.RunID, req.Node.ID(), d)
					r.emitConcWaitLog(ctx, req, d)
				}
			}
			continue
		case store.WaiterPromoted:
			_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "concurrency_promoted", nil)
			// Clear the "queued ..." detail now that the node holds a slot.
			_ = r.backends.State.UpdateNodeActivity(ctx, req.RunID, req.Node.ID(), "")
			return r.runHeldSlot(ctx, req, cp, res.HolderID)
		case store.WaiterCached:
			return r.applyCacheHit(ctx, req, cp, res.OutputRef, res.OriginRunID, res.OriginNodeID)
		case store.WaiterLeaderFinished:
			return r.inheritLeaderOutcome(ctx, req, cp, res.LeaderRunID, res.LeaderNodeID)
		case store.WaiterCancelled:
			err := fmt.Errorf("concurrency key %q: waiter was cancelled or superseded", cp.key)
			_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "concurrency_cancelled", nil)
			_ = r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(sparkwing.Superseded), err.Error(), nil)
			return runner.Result{Outcome: sparkwing.Superseded, Err: err}
		}
	}
}

// failQueueTimeout cleans up a waiter that exhausted its QueueTimeout:
// it drops the parked waiter row so a later release can't promote a
// node that already gave up, then finalizes the node as failed with
// failure_reason "queue_timeout".
func (r *InProcessRunner) failQueueTimeout(ctx context.Context, req runner.Request, cp coordParams) runner.Result {
	if _, err := r.backends.Concurrency.CancelWaiter(ctx, cp.key, req.RunID, req.Node.ID()); err != nil {
		slog.Warn("cancel waiter after queue timeout failed; reaper will sweep it",
			"key", cp.key, "run", req.RunID, "node", req.Node.ID(), "err", err)
	}
	err := fmt.Errorf("concurrency key %q: queued %s without a slot under OnLimit:Queue", cp.key, cp.queueTimeout)
	payload, _ := json.Marshal(map[string]any{
		"key":           cp.key,
		"queue_timeout": cp.queueTimeout.String(),
	})
	_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "concurrency_queue_timeout", payload)
	_ = r.backends.State.FinishNodeWithReason(ctx, req.RunID, req.Node.ID(),
		string(sparkwing.Failed), err.Error(), nil, store.FailureQueueTimeout, nil)
	return runner.Result{Outcome: sparkwing.Failed, Err: err}
}

// inheritLeaderOutcome adopts the leader's terminal outcome + output
// when it finished without writing a cache entry. Failed leaders
// produce failed followers.
func (r *InProcessRunner) inheritLeaderOutcome(ctx context.Context, req runner.Request, cp coordParams, leaderRunID, leaderNodeID string) runner.Result {
	output, err := r.backends.State.GetNodeOutput(ctx, leaderRunID, leaderNodeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		r.markFailed(ctx, req.RunID, req.Node.ID(), fmt.Errorf("fetch leader output: %w", err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}

	_ = r.backends.State.StartNode(ctx, req.RunID, req.Node.ID())
	payload, _ := json.Marshal(map[string]any{
		"key":            cp.key,
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
