package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var slotObservationIntervalNanos atomic.Int64

func supersessionPollInterval() time.Duration {
	if interval := time.Duration(slotObservationIntervalNanos.Load()); interval > 0 {
		return interval
	}
	return store.DefaultConcurrencyHeartbeatInterval
}

func slotHeartbeatInterval(onLimit string) time.Duration {
	if interval := time.Duration(slotObservationIntervalNanos.Load()); interval > 0 {
		return interval
	}
	return store.ConcurrencyHeartbeatInterval(onLimit)
}

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

func concQueuedDetail(namespace string, position int, holders []store.ConcurrencyHolder) string {
	return fmt.Sprintf("queued in %s: %d ahead, held by %s", namespace, position, heldByLabel(holders))
}

func heldByLabel(holders []store.ConcurrencyHolder) string {
	if len(holders) == 0 {
		return "unknown"
	}
	held := holderLabel(holders[0].RunID, holders[0].NodeID)
	if extra := len(holders) - 1; extra > 0 {
		held = fmt.Sprintf("%s +%d", held, extra)
	}
	return held
}

func (r *NodeExecutor) emitConcWaitLog(ctx context.Context, req runner.Request, detail string) {
	if nodeLog, err := r.backends.Logs.OpenNodeLog(ctx, req.RunID, req.Node.ID(), req.Delegate); err == nil {
		nodeLog.Emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Event: "concurrency_wait", Msg: detail})
		if err := nodeLog.Close(); err != nil {
			slog.Error("close node log failed", "run", req.RunID, "node", req.Node.ID(), "err", err)
		}
	} else {
		slog.Error("open node log failed", "run", req.RunID, "node", req.Node.ID(), "err", err)
	}
}

func holderLabel(runID, nodeID string) string {
	if nodeID == "" {
		return runID
	}
	return runID + "/" + nodeID
}

const memoKeyPrefix = "memo:"

func memoKeyFor(contentHash string) string { return memoKeyPrefix + contentHash }

const (
	scopeKeyGlobalPrefix = "g:"
	scopeKeyRunPrefix    = "r:"
	scopeKeyBoxPrefix    = "b:"
	scopeKeyLenSep       = ":"
)

func boxHostID() string {
	if v := strings.TrimSpace(os.Getenv("SPARKWING_BOX_ID")); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return "localhost"
}

func scopedGroupKey(g *sparkwing.ConcurrencyGroup, runID string) string {
	name := g.Name()
	switch g.Limit().Scope {
	case sparkwing.ScopeRun:
		return qualifiedKey(scopeKeyRunPrefix, runID, name)
	case sparkwing.ScopeBox:
		return qualifiedKey(scopeKeyBoxPrefix, boxHostID(), name)
	default:
		return scopeKeyGlobalPrefix + name
	}
}

func qualifiedKey(prefix, qualifier, name string) string {
	return prefix + strconv.Itoa(len(qualifier)) + scopeKeyLenSep + qualifier + name
}

func qualifierFromKey(rest string) string {
	sep := strings.IndexByte(rest, scopeKeyLenSep[0])
	if sep < 0 {
		return ""
	}
	n, err := strconv.Atoi(rest[:sep])
	if err != nil || n < 0 || sep+1+n > len(rest) {
		return ""
	}
	return rest[sep+1 : sep+1+n]
}

func ScopeLabelFromKey(key string) string {
	switch {
	case strings.HasPrefix(key, memoKeyPrefix):
		return "content-cache"
	case strings.HasPrefix(key, scopeKeyGlobalPrefix):
		return "global"
	case strings.HasPrefix(key, scopeKeyRunPrefix):
		return "run (" + qualifierFromKey(key[len(scopeKeyRunPrefix):]) + ")"
	case strings.HasPrefix(key, scopeKeyBoxPrefix):
		return "box (" + qualifierFromKey(key[len(scopeKeyBoxPrefix):]) + ")"
	default:
		return "global"
	}
}

type coordinationParameters struct {
	key           string
	capacity      int
	cost          int
	policy        string
	cacheHash     string
	cacheTTL      time.Duration
	cancelTimeout time.Duration
	queueTimeout  time.Duration
}

func concurrencyParametersFor(node *sparkwing.JobNode, g *sparkwing.ConcurrencyGroup, runID string) coordinationParameters {
	lim := g.Limit()
	return coordinationParameters{
		key:           scopedGroupKey(g, runID),
		capacity:      lim.Capacity,
		cost:          node.ConcurrencyCost(),
		policy:        string(lim.OnLimit),
		cancelTimeout: lim.CancelTimeout,
		queueTimeout:  lim.QueueTimeout,
	}
}

func memoParamsFor(cacheHash string, cacheTTL time.Duration) coordinationParameters {
	return coordinationParameters{
		key:       memoKeyFor(cacheHash),
		capacity:  1,
		cost:      1,
		policy:    store.OnLimitCoalesce,
		cacheHash: cacheHash,
		cacheTTL:  cacheTTL,
	}
}

func (parameters coordinationParameters) acquireRequest(runID, nodeID string, bypassRead bool) store.AcquireSlotRequest {
	return store.AcquireSlotRequest{
		Key:           parameters.key,
		HolderID:      runID + "/" + nodeID,
		RunID:         runID,
		NodeID:        nodeID,
		Capacity:      parameters.capacity,
		Cost:          parameters.cost,
		Policy:        parameters.policy,
		CacheKeyHash:  parameters.cacheHash,
		CacheTTL:      parameters.cacheTTL,
		CancelTimeout: parameters.cancelTimeout,
		BypassRead:    bypassRead,
	}
}

func (r *NodeExecutor) runNodeWithCache(ctx context.Context, req runner.Request) (runner.Result, bool) {
	node := req.Node
	group := node.ConcurrencyGroupRef()
	cacheConfig := node.MemoizeConfig()
	if group == nil && cacheConfig == nil {
		return runner.Result{}, false
	}

	cacheHash, cacheTTL, err := r.resolveCacheHash(ctx, node, cacheConfig)
	if err != nil {
		r.markFailedIfUnfinished(ctx, req.RunID, node.ID(), err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}, true
	}
	hasMemo := cacheHash != ""

	switch {
	case hasMemo && group != nil:
		return r.runMemoizedUnderConcurrency(ctx, req, group, cacheHash, cacheTTL), true
	case hasMemo:
		return r.acquireAndRun(ctx, req, memoParamsFor(cacheHash, cacheTTL)), true
	case group != nil:
		return r.runUnderGroup(ctx, req, group), true
	default:
		return runner.Result{}, false
	}
}

func (r *NodeExecutor) runUnderGroup(ctx context.Context, req runner.Request, group *sparkwing.ConcurrencyGroup) runner.Result {
	if la, _, _ := localAdmissionFromContext(ctx); la != nil && groupUsesLocalDaemon(group) {
		return r.runNodeUnderDaemonSem(ctx, req, la, group)
	}
	return r.acquireAndRun(ctx, req, concurrencyParametersFor(req.Node, group, req.RunID))
}

func (r *NodeExecutor) resolveCacheHash(ctx context.Context, node *sparkwing.JobNode, cacheConfig *sparkwing.MemoizeConfig) (string, time.Duration, error) {
	if cacheConfig == nil {
		return "", 0, nil
	}
	key, err := resolveCacheKey(ctx, cacheConfig.Key, node.ID())
	if err != nil {
		return "", 0, err
	}
	if key == sparkwing.NoCache {
		sparkwing.LoggerFromContext(ctx).Log("info", fmt.Sprintf("Memoize(%s) returned NoCache; memoization skipped", node.ID()))
		return "", cacheConfig.TTL, nil
	}
	return string(key), cacheConfig.TTL, nil
}

func (r *NodeExecutor) acquireAndRun(ctx context.Context, req runner.Request, parameters coordinationParameters) runner.Result {
	node := req.Node
	holderID := fmt.Sprintf("%s/%s", req.RunID, node.ID())
	wedgeBudget, err := storeWedgeBudget()
	if err != nil {
		r.markFailed(ctx, req.RunID, node.ID(), err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	resp, err := r.backends.Concurrency.AcquireSlot(ctx, parameters.acquireRequest(req.RunID, node.ID(), noCacheFromContext(ctx)))
	if err != nil {
		r.markFailed(ctx, req.RunID, node.ID(), fmt.Errorf("concurrency acquire(%q): %w", parameters.key, err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}

	if resp.DriftNote != "" {
		payload, _ := json.Marshal(map[string]any{
			"key":               parameters.key,
			"previous_capacity": resp.PreviousCapacity,
			"new_capacity":      parameters.capacity,
			"note":              resp.DriftNote,
		})
		noteEvent(ctx, r.backends.State, req.RunID, node.ID(), "concurrency_drift", payload)
		slog.Default().Warn("concurrency drift", "key", parameters.key, "prev", resp.PreviousCapacity, "new", parameters.capacity)
	}

	switch resp.Kind {
	case store.AcquireCached:
		return r.applyCacheHit(ctx, req, parameters, resp.OriginRunID, resp.OriginNodeID)
	case store.AcquireSkipped:
		return r.applySkippedConcurrent(ctx, req)
	case store.AcquireFailed:
		err := fmt.Errorf("concurrency key %q slot full under OnLimit:Fail", parameters.key)
		r.markFailed(ctx, req.RunID, node.ID(), err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	case store.AcquireGranted:
		return r.runHeldSlot(ctx, req, parameters, holderID, wedgeBudget)
	case store.AcquireQueued, store.AcquireCoalesced, store.AcquireCancellingOthers:
		return r.waitThenRun(ctx, req, parameters, resp, wedgeBudget)
	}

	err = fmt.Errorf("concurrency acquire returned unknown kind %q", resp.Kind)
	r.markFailed(ctx, req.RunID, node.ID(), err)
	return runner.Result{Outcome: sparkwing.Failed, Err: err}
}

func (r *NodeExecutor) runMemoizedUnderConcurrency(ctx context.Context, req runner.Request, group *sparkwing.ConcurrencyGroup, cacheHash string, cacheTTL time.Duration) runner.Result {
	node := req.Node
	memoParameters := memoParamsFor(cacheHash, cacheTTL)
	memoHolderID := fmt.Sprintf("%s/%s", req.RunID, node.ID())
	wedgeBudget, err := storeWedgeBudget()
	if err != nil {
		r.markFailed(ctx, req.RunID, node.ID(), err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	resp, err := r.backends.Concurrency.AcquireSlot(ctx, memoParameters.acquireRequest(req.RunID, node.ID(), noCacheFromContext(ctx)))
	if err != nil {
		r.markFailed(ctx, req.RunID, node.ID(), fmt.Errorf("memo acquire(%q): %w", memoParameters.key, err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}

	switch resp.Kind {
	case store.AcquireCached:
		return r.applyCacheHit(ctx, req, memoParameters, resp.OriginRunID, resp.OriginNodeID)
	case store.AcquireCoalesced:
		return r.waitThenRun(ctx, req, memoParameters, resp, wedgeBudget)
	case store.AcquireQueued:
		return r.waitThenRun(ctx, req, memoParameters, resp, wedgeBudget)
	case store.AcquireGranted:
		execCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var lost atomic.Bool
		var lostWedge atomic.Pointer[string]
		stopHeartbeat := r.startSlotHeartbeat(execCtx, memoParameters.key, memoHolderID, memoParameters.policy, &lost, &lostWedge, cancel, wedgeBudget)

		result := r.runUnderGroup(execCtx, req, group)

		stopHeartbeat()
		releaseContext, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelRelease()
		if err := r.backends.Concurrency.ReleaseSlot(releaseContext, memoParameters.key, memoHolderID,
			storeOutcome(result), fmt.Sprintf("%s/%s", req.RunID, node.ID()), cacheHash, cacheTTL); err != nil {
			slog.Warn("memo release failed; relying on reaper", "key", memoParameters.key, "err", err)
		}
		return result
	default:
		err := fmt.Errorf("memo acquire(%q) returned unexpected kind %q", memoParameters.key, resp.Kind)
		r.markFailed(ctx, req.RunID, node.ID(), err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
}

func storeOutcome(res runner.Result) string {
	switch res.Outcome {
	case sparkwing.Success, sparkwing.Cached:
		return "success"
	case sparkwing.Skipped, sparkwing.SkippedConcurrent:
		return "skipped"
	case sparkwing.Superseded:
		return "superseded"
	default:
		return "failed"
	}
}

func (r *NodeExecutor) applyCacheHit(ctx context.Context, req runner.Request, parameters coordinationParameters, originRun, originNode string) runner.Result {
	output, err := r.fetchCachedOutput(ctx, originRun, originNode)
	if err != nil {
		r.markFailed(ctx, req.RunID, req.Node.ID(), fmt.Errorf("cache hit: fetch output: %w", err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}

	_ = r.backends.State.StartNode(ctx, req.RunID, req.Node.ID())
	payload, _ := json.Marshal(map[string]any{
		"key":            parameters.key,
		"cache_key_hash": parameters.cacheHash,
		"origin_run_id":  originRun,
		"origin_node_id": originNode,
	})
	noteEvent(ctx, r.backends.State, req.RunID, req.Node.ID(), "cache_hit", payload)
	r.copyArtifactManifest(ctx, req.RunID, req.Node.ID(), originRun, originNode)
	if err := r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(sparkwing.Cached), "", output); err != nil {
		noteLostStateWrite(ctx, "finish node", req.RunID, err)
	}

	if nodeLog, err := r.backends.Logs.OpenNodeLog(ctx, req.RunID, req.Node.ID(), req.Delegate); err == nil {
		nodeLog = wrapNodeLogWithMasker(nodeLog, secrets.MaskerFromContext(ctx))
		ts := time.Now()
		nodeLog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_start", Attrs: map[string]any{"cache_hit": true}})
		nodeLog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_end", Attrs: map[string]any{
			"outcome": string(sparkwing.Cached), "duration_ms": int64(0), "cache_hit": true,
		}})
		if err := nodeLog.Close(); err != nil {
			slog.Error("close node log failed", "run", req.RunID, "node", req.Node.ID(), "err", err)
		}
	} else {
		slog.Error("open node log failed", "run", req.RunID, "node", req.Node.ID(), "err", err)
	}

	return runner.Result{Outcome: sparkwing.Cached, Output: output}
}

func (r *NodeExecutor) applySkippedConcurrent(ctx context.Context, req runner.Request) runner.Result {
	_ = r.backends.State.StartNode(ctx, req.RunID, req.Node.ID())
	noteEvent(ctx, r.backends.State, req.RunID, req.Node.ID(), "node_skipped_concurrent", nil)
	if err := r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(sparkwing.SkippedConcurrent), "", nil); err != nil {
		noteLostStateWrite(ctx, "finish node", req.RunID, err)
	}

	if nodeLog, err := r.backends.Logs.OpenNodeLog(ctx, req.RunID, req.Node.ID(), req.Delegate); err == nil {
		nodeLog = wrapNodeLogWithMasker(nodeLog, secrets.MaskerFromContext(ctx))
		ts := time.Now()
		nodeLog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_start"})
		nodeLog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_end", Attrs: map[string]any{
			"outcome": string(sparkwing.SkippedConcurrent), "duration_ms": int64(0),
		}})
		if err := nodeLog.Close(); err != nil {
			slog.Error("close node log failed", "run", req.RunID, "node", req.Node.ID(), "err", err)
		}
	} else {
		slog.Error("open node log failed", "run", req.RunID, "node", req.Node.ID(), "err", err)
	}
	return runner.Result{Outcome: sparkwing.SkippedConcurrent}
}

func (r *NodeExecutor) runHeldSlot(ctx context.Context, req runner.Request, parameters coordinationParameters, holderID string, wedgeBudget time.Duration) runner.Result {
	execCtx, cancelExec := context.WithCancel(ctx)
	var superseded atomic.Bool
	var wedgeAbort atomic.Pointer[string]
	stopHeartbeat := r.startSlotHeartbeat(execCtx, parameters.key, holderID, parameters.policy, &superseded, &wedgeAbort, cancelExec, wedgeBudget)

	defer func() {
		stopHeartbeat()
		cancelExec()
		ctxBG, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		outcome := r.lastReleaseOutcomeFor(req.RunID, req.Node.ID())
		outputRef := fmt.Sprintf("%s/%s", req.RunID, req.Node.ID())
		if err := r.backends.Concurrency.ReleaseSlot(ctxBG, parameters.key, holderID, outcome, outputRef, parameters.cacheHash, parameters.cacheTTL); err != nil {
			slog.Warn("concurrency release failed; relying on reaper",
				"key", parameters.key, "holder_id", holderID, "err", err)
		}
	}()

	if reason, skip := evalSkipPredicates(execCtx, req.Node); skip {
		r.markSkipped(execCtx, req.RunID, req.Node.ID(), reason)
		r.recordReleaseOutcome(req.RunID, req.Node.ID(), string(sparkwing.Skipped))
		return runner.Result{Outcome: sparkwing.Skipped}
	}

	output, err := r.executeNodeWithAdmission(execCtx, req)
	if reason := wedgeAbort.Load(); reason != nil {
		werr := errors.New(*reason)
		noteEvent(ctx, r.backends.State, req.RunID, req.Node.ID(), "node_store_wedged", []byte(werr.Error()))
		r.recordReleaseOutcome(req.RunID, req.Node.ID(), string(sparkwing.Failed))
		return runner.Result{Outcome: sparkwing.Failed, Err: werr}
	}
	if superseded.Load() {
		err := fmt.Errorf("concurrency key %q: holder superseded by newer arrival", parameters.key)
		noteEvent(ctx, r.backends.State, req.RunID, req.Node.ID(), "node_superseded", []byte(err.Error()))
		if err := r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(sparkwing.Superseded), err.Error(), nil); err != nil {
			noteLostStateWrite(ctx, "finish node", req.RunID, err)
		}
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

func (r *NodeExecutor) startSlotHeartbeat(ctx context.Context, key, holderID, onLimit string, superseded *atomic.Bool, wedgeAbort *atomic.Pointer[string], cancelExec context.CancelFunc, wedgeBudget time.Duration) func() {
	done := make(chan struct{})
	var once sync.Once

	lease := store.DefaultConcurrencyLease
	var supersessionTicker *time.Ticker
	var supersessionC <-chan time.Time
	if pollsForSupersession(onLimit) {
		supersessionTicker = time.NewTicker(supersessionPollInterval())
		supersessionC = supersessionTicker.C
	}

	go func() {
		wedge := newStoreWedgeGuard(wedgeBudget)
		t := time.NewTicker(slotHeartbeatInterval(onLimit))
		defer t.Stop()
		if supersessionTicker != nil {
			defer supersessionTicker.Stop()
		}
		lastOK := time.Now()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-supersessionC:
				pollCtx, cancel := context.WithTimeout(context.Background(), store.DefaultConcurrencyHeartbeatTimeout)
				holder, err := r.backends.Concurrency.ObserveSlot(pollCtx, key, holderID)
				cancel()
				if slotOwnershipLost(holder, err) {
					superseded.Store(true)
					cancelExec()
					return
				}
				continue
			case <-t.C:
				hbCtx, cancel := context.WithTimeout(context.Background(), store.ConcurrencyHeartbeatTimeout(onLimit))
				_, wasSuperseded, err := r.backends.Concurrency.HeartbeatSlot(hbCtx, key, holderID, lease)
				cancel()
				if errors.Is(err, store.ErrLockHeld) {
					superseded.Store(true)
					cancelExec()
					return
				}
				if err != nil {
					sinceOK := time.Since(lastOK)
					if terminal := wedge.fail(fmt.Sprintf("concurrency key %q: heartbeat", key), err); terminal != nil {
						slog.Error("concurrency store wedged; aborting work",
							"key", key, "holder", holderID, "err", terminal)
						msg := terminal.Error()
						wedgeAbort.Store(&msg)
						cancelExec()
						return
					}
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
				wedge.success()
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

func pollsForSupersession(onLimit string) bool {
	return onLimit != store.OnLimitCancelOthers
}

func slotOwnershipLost(holder *store.ConcurrencyHolder, err error) bool {
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	if err != nil {
		return false
	}
	return holder == nil || holder.Superseded
}

func (r *NodeExecutor) waitThenRun(ctx context.Context, req runner.Request, parameters coordinationParameters, initial store.AcquireSlotResponse, wedgeBudget time.Duration) runner.Result {
	wedge := newStoreWedgeGuard(wedgeBudget)
	leaderRun, leaderNode := initial.LeaderRunID, initial.LeaderNodeID

	holders := make([]map[string]string, 0, len(initial.Holders))
	for _, h := range initial.Holders {
		holders = append(holders, map[string]string{"run_id": h.RunID, "node_id": h.NodeID})
	}
	payload, _ := json.Marshal(map[string]any{
		"key":            parameters.key,
		"kind":           string(initial.Kind),
		"position":       initial.Position,
		"queue_length":   initial.QueueLength,
		"holders":        holders,
		"leader_run_id":  leaderRun,
		"leader_node_id": leaderNode,
	})
	noteEvent(ctx, r.backends.State, req.RunID, req.Node.ID(), "concurrency_wait", payload)

	lastDetail := concWaitDetail(parameters.key, initial, leaderRun, leaderNode)
	if lastDetail != "" {
		if err := r.backends.State.UpdateNodeActivity(ctx, req.RunID, req.Node.ID(), lastDetail); err != nil {
			noteLostStateWrite(ctx, "update node activity", req.RunID, err)
		}
		r.emitConcWaitLog(ctx, req, lastDetail)
	}
	queueRefresh := initial.Kind == store.AcquireQueued

	if initial.Kind == store.AcquireCancellingOthers && parameters.cancelTimeout > 0 {
		timer := time.AfterFunc(parameters.cancelTimeout, func() {
			cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			dropped, err := r.backends.Concurrency.ForceReleaseSuperseded(cleanupContext, parameters.key)
			if err != nil {
				slog.Warn("force-release after cancel timeout failed", "key", parameters.key, "err", err)
				return
			}
			if len(dropped) > 0 {
				dropPayload, _ := json.Marshal(map[string]any{
					"key":     parameters.key,
					"count":   len(dropped),
					"reason":  "cancel_timeout",
					"timeout": parameters.cancelTimeout.String(),
				})
				if err := r.backends.State.AppendEvent(cleanupContext, req.RunID, req.Node.ID(), "concurrency_force_release", dropPayload); err != nil {
					slog.Error("record forced concurrency release failed", "run", req.RunID, "node", req.Node.ID(), "key", parameters.key, "err", err)
				}
			}
		})
		defer timer.Stop()
	}

	var queueDeadline time.Time
	if parameters.queueTimeout > 0 && initial.Kind == store.AcquireQueued {
		queueDeadline = time.Now().Add(parameters.queueTimeout)
	}

	if req.ReleaseWorkerSlot != nil {
		req.ReleaseWorkerSlot()
	}

	const pollInterval = 100 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if _, err := r.backends.Concurrency.CancelWaiter(cleanupContext, parameters.key, req.RunID, req.Node.ID()); err != nil {
				slog.Warn("cancel waiter on context cancellation failed; reaper will sweep it",
					"key", parameters.key, "run", req.RunID, "node", req.Node.ID(), "err", err)
			}
			cancel()
			r.markFailed(ctx, req.RunID, req.Node.ID(), ctx.Err())
			return runner.Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
		case <-ticker.C:
		}

		res, err := r.backends.Concurrency.ResolveWaiter(ctx, parameters.key, req.RunID, req.Node.ID(), parameters.cacheHash, leaderRun, leaderNode, noCacheFromContext(ctx))
		if err != nil {
			terminal := wedge.fail(fmt.Sprintf("concurrency key %q: resolve waiter", parameters.key), err)
			if terminal == nil {
				continue
			}
			r.markFailed(ctx, req.RunID, req.Node.ID(), terminal)
			return runner.Result{Outcome: sparkwing.Failed, Err: terminal}
		}
		wedge.success()

		switch res.Status {
		case store.WaiterStillWaiting:
			if !queueDeadline.IsZero() && time.Now().After(queueDeadline) {
				return r.failQueueTimeout(ctx, req, parameters)
			}
			if queueRefresh {
				if d := concQueuedDetail(parameters.key, res.Position, res.Holders); d != lastDetail {
					lastDetail = d
					if err := r.backends.State.UpdateNodeActivity(ctx, req.RunID, req.Node.ID(), d); err != nil {
						noteLostStateWrite(ctx, "update node activity", req.RunID, err)
					}
					r.emitConcWaitLog(ctx, req, d)
				}
			}
			continue
		case store.WaiterPromoted:
			return r.runPromotedSlot(ctx, req, parameters, res.HolderID, wedgeBudget)
		case store.WaiterCached:
			return r.applyCacheHit(ctx, req, parameters, res.OriginRunID, res.OriginNodeID)
		case store.WaiterLeaderFinished:
			return r.inheritLeaderOutcome(ctx, req, parameters, res.LeaderRunID, res.LeaderNodeID, res.LeaderOutcome, res.LeaderFailureReason)
		case store.WaiterCancelled:
			err := fmt.Errorf("concurrency key %q: waiter was cancelled or superseded", parameters.key)
			noteEvent(ctx, r.backends.State, req.RunID, req.Node.ID(), "concurrency_cancelled", nil)
			if err := r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(sparkwing.Superseded), err.Error(), nil); err != nil {
				noteLostStateWrite(ctx, "finish node", req.RunID, err)
			}
			return runner.Result{Outcome: sparkwing.Superseded, Err: err}
		}
	}
}

func (r *NodeExecutor) runPromotedSlot(ctx context.Context, req runner.Request, parameters coordinationParameters, holderID string, wedgeBudget time.Duration) runner.Result {
	// safety: promotion already granted this holder, and only
	// runHeldSlot releases it, so every exit short of that hand-off
	// gives the slot back here.
	handedOff := false
	defer func() {
		if handedOff {
			return
		}
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := r.backends.Concurrency.ReleaseSlot(cleanupContext, parameters.key, holderID, "cancelled", "", "", 0); err != nil {
			slog.Warn("promoted holder release failed; relying on reaper",
				"key", parameters.key, "holder_id", holderID, "err", err)
		}
	}()

	if req.ReacquireWorkerSlot != nil && !req.ReacquireWorkerSlot() {
		r.markFailed(ctx, req.RunID, req.Node.ID(), context.Canceled)
		return runner.Result{Outcome: sparkwing.Cancelled}
	}
	noteEvent(ctx, r.backends.State, req.RunID, req.Node.ID(), "concurrency_promoted", nil)
	if err := r.backends.State.UpdateNodeActivity(ctx, req.RunID, req.Node.ID(), ""); err != nil {
		noteLostStateWrite(ctx, "update node activity", req.RunID, err)
	}

	handedOff = true
	return r.runHeldSlot(ctx, req, parameters, holderID, wedgeBudget)
}

func (r *NodeExecutor) failQueueTimeout(ctx context.Context, req runner.Request, parameters coordinationParameters) runner.Result {
	if _, err := r.backends.Concurrency.CancelWaiter(ctx, parameters.key, req.RunID, req.Node.ID()); err != nil {
		slog.Warn("cancel waiter after queue timeout failed; reaper will sweep it",
			"key", parameters.key, "run", req.RunID, "node", req.Node.ID(), "err", err)
	}
	err := fmt.Errorf("concurrency key %q: queued %s without a slot under OnLimit:Queue", parameters.key, parameters.queueTimeout)
	payload, _ := json.Marshal(map[string]any{
		"key":           parameters.key,
		"queue_timeout": parameters.queueTimeout.String(),
	})
	noteEvent(ctx, r.backends.State, req.RunID, req.Node.ID(), "concurrency_queue_timeout", payload)
	if ferr := r.backends.State.FinishNodeWithReason(ctx, req.RunID, req.Node.ID(),
		string(sparkwing.Failed), err.Error(), nil, store.FailureQueueTimeout, nil); ferr != nil {
		noteLostStateWrite(ctx, "finish node", req.RunID, ferr)
	}
	return runner.Result{Outcome: sparkwing.Failed, Err: err}
}

func followerOutcomeFromLeader(leaderOutcome string) sparkwing.Outcome {
	switch leaderOutcome {
	case string(sparkwing.Success), string(sparkwing.Cached):
		return sparkwing.Success
	case string(sparkwing.Skipped), string(sparkwing.SkippedConcurrent):
		return sparkwing.Skipped
	case string(sparkwing.Superseded):
		return sparkwing.Superseded
	case string(sparkwing.Cancelled):
		return sparkwing.Cancelled
	default:
		return sparkwing.Failed
	}
}

func (r *NodeExecutor) inheritLeaderOutcome(ctx context.Context, req runner.Request, parameters coordinationParameters, leaderRunID, leaderNodeID, leaderOutcome, leaderFailureReason string) runner.Result {
	output, err := r.backends.State.GetNodeOutput(ctx, leaderRunID, leaderNodeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		r.markFailed(ctx, req.RunID, req.Node.ID(), fmt.Errorf("fetch leader output: %w", err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}

	_ = r.backends.State.StartNode(ctx, req.RunID, req.Node.ID())
	payload, _ := json.Marshal(map[string]any{
		"key":            parameters.key,
		"leader_run_id":  leaderRunID,
		"leader_node_id": leaderNodeID,
		"leader_outcome": leaderOutcome,
	})
	noteEvent(ctx, r.backends.State, req.RunID, req.Node.ID(), "coalesced", payload)
	r.copyArtifactManifest(ctx, req.RunID, req.Node.ID(), leaderRunID, leaderNodeID)

	outcome := followerOutcomeFromLeader(leaderOutcome)
	if outcome == sparkwing.Failed {
		if err := r.backends.State.FinishNodeWithReason(ctx, req.RunID, req.Node.ID(), string(outcome), "", output, leaderFailureReason, nil); err != nil {
			noteLostStateWrite(ctx, "finish node", req.RunID, err)
		}
	} else {
		if err := r.backends.State.FinishNode(ctx, req.RunID, req.Node.ID(), string(outcome), "", output); err != nil {
			noteLostStateWrite(ctx, "finish node", req.RunID, err)
		}
	}

	if nodeLog, err := r.backends.Logs.OpenNodeLog(ctx, req.RunID, req.Node.ID(), req.Delegate); err == nil {
		nodeLog = wrapNodeLogWithMasker(nodeLog, secrets.MaskerFromContext(ctx))
		ts := time.Now()
		nodeLog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_start", Attrs: map[string]any{
			"coalesced_from": fmt.Sprintf("%s/%s", leaderRunID, leaderNodeID),
		}})
		nodeLog.Emit(sparkwing.LogRecord{TS: ts, Level: "info", Event: "node_end", Attrs: map[string]any{
			"outcome": string(outcome), "duration_ms": int64(0),
			"coalesced_from": fmt.Sprintf("%s/%s", leaderRunID, leaderNodeID),
		}})
		if err := nodeLog.Close(); err != nil {
			slog.Error("close node log failed", "run", req.RunID, "node", req.Node.ID(), "err", err)
		}
	} else {
		slog.Error("open node log failed", "run", req.RunID, "node", req.Node.ID(), "err", err)
	}
	return runner.Result{Outcome: outcome, Output: output}
}

func (r *NodeExecutor) fetchCachedOutput(ctx context.Context, originRun, originNode string) ([]byte, error) {
	return r.backends.State.GetNodeOutput(ctx, originRun, originNode)
}

func (r *NodeExecutor) copyArtifactManifest(ctx context.Context, dstRun, dstNode, srcRun, srcNode string) {
	src, err := r.backends.State.GetNode(ctx, srcRun, srcNode)
	if err != nil || src == nil || src.ArtifactManifest == "" {
		return
	}
	_ = r.backends.State.SetNodeArtifactManifest(ctx, dstRun, dstNode, src.ArtifactManifest)
}

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

func (i *inflightMap) take(runID, nodeID string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	outcome, ok := i.m[runID+"/"+nodeID]
	if !ok {
		return "success"
	}
	delete(i.m, runID+"/"+nodeID)
	return outcome
}

func (r *NodeExecutor) recordReleaseOutcome(runID, nodeID, outcome string) {
	inflightOutcomes.set(runID, nodeID, outcome)
}

func (r *NodeExecutor) lastReleaseOutcomeFor(runID, nodeID string) string {
	return inflightOutcomes.take(runID, nodeID)
}
