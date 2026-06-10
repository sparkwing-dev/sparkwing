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

// memoKeyPrefix namespaces content-addressed memoization slots so they
// can never collide with an author-named concurrency group. A cached
// node coordinates on memoKeyPrefix+contentHash: identical content
// shares one leader (in-flight dedupe) and one cache row regardless of
// which concurrency group -- if any -- the node also belongs to. Memo
// and concurrency are independent store interactions on a node that
// declares both.
const memoKeyPrefix = "memo:"

func memoKeyFor(contentHash string) string { return memoKeyPrefix + contentHash }

// Scope-qualified coordination-key scheme. Each scope gets a distinct
// leading tag, and Run/Box keys length-prefix their qualifier (run id
// or host) before the group name. Both pieces are author- or
// operator-supplied and may contain any byte, including the separators,
// so the encoding must stay injective in (scope, qualifier, name): the
// length prefix makes the qualifier/name boundary unambiguous, and the
// leading tag keeps scopes from colliding (a Global name can never fold
// onto a Box or Run key). The tag also lets the CLI label a key's scope.
const (
	scopeKeyGlobalPrefix = "g:"
	scopeKeyRunPrefix    = "r:"
	scopeKeyBoxPrefix    = "b:"
	scopeKeyLenSep       = ":" // separates the qualifier byte-length from the qualifier
)

// boxHostID is the stable host identity used to qualify ScopeBox keys.
// It defaults to os.Hostname() and is overridable via SPARKWING_BOX_ID
// for environments where the hostname is unstable or shared.
func boxHostID() string {
	if v := strings.TrimSpace(os.Getenv("SPARKWING_BOX_ID")); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return "localhost"
}

// scopedGroupKey folds a group's Scope into its coordination key:
// ScopeRun isolates per run, ScopeBox pools per machine, ScopeGlobal
// (the zero value) pools across the fleet by bare name.
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

// qualifiedKey builds a Run/Box coordination key as
// <prefix><len><sep><qualifier><name>, length-prefixing the qualifier
// so a qualifier or name containing the separator can't fold two
// distinct identities onto the same key.
func qualifiedKey(prefix, qualifier, name string) string {
	return prefix + strconv.Itoa(len(qualifier)) + scopeKeyLenSep + qualifier + name
}

// qualifierFromKey recovers the length-prefixed qualifier from the
// remainder of a Run/Box key (the bytes after its scheme tag), or ""
// if the prefix is malformed.
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

// ScopeLabelFromKey reports a human label for the scope a coordination
// key encodes, for the CLI / dashboard. The leading scheme tag is
// authoritative; the qualifier (run id or host) is surfaced when
// present. A "memo:" key is the content-addressed memoization slot, not
// a group.
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

// coordParams is the resolved coordination input for one store acquire
// (a concurrency group slot or a content-memo slot).
type coordParams struct {
	key           string
	capacity      int
	cost          int
	policy        string
	cacheHash     string
	cacheTTL      time.Duration
	cancelTimeout time.Duration
	queueTimeout  time.Duration
}

// concParamsFor builds the coordParams for a node's concurrency group:
// scope-qualified key, capacity, policy, cost, and timeout knobs. No
// cache hash -- memoization is a separate acquire.
func concParamsFor(node *sparkwing.JobNode, g *sparkwing.ConcurrencyGroup, runID string) coordParams {
	lim := g.Limit()
	return coordParams{
		key:           scopedGroupKey(g, runID),
		capacity:      lim.Capacity,
		cost:          node.ConcurrencyCost(),
		policy:        string(lim.OnLimit),
		cancelTimeout: lim.CancelTimeout,
		queueTimeout:  lim.QueueTimeout,
	}
}

// runNodeWithCache owns the full Cache()/Concurrency() lifecycle.
// Memoization (content-keyed) and concurrency admission (group-keyed)
// are independent: a node may have either, both, or neither. Returns
// handled=false when the node needs no coordination so the caller runs
// it on the normal path.
func (r *InProcessRunner) runNodeWithCache(ctx context.Context, req runner.Request) (runner.Result, bool) {
	node := req.Node
	group := node.ConcurrencyGroupRef()
	cacheCfg := node.CacheConfig()
	if group == nil && cacheCfg == nil {
		return runner.Result{}, false
	}

	cacheHash, cacheTTL := r.resolveCacheHash(ctx, node, cacheCfg)
	hasMemo := cacheHash != ""

	switch {
	case hasMemo && group != nil:
		return r.runMemoizedUnderConcurrency(ctx, req, group, cacheHash, cacheTTL), true
	case hasMemo:
		memoCP := coordParams{
			key:       memoKeyFor(cacheHash),
			capacity:  1,
			cost:      1,
			policy:    store.OnLimitCoalesce,
			cacheHash: cacheHash,
			cacheTTL:  cacheTTL,
		}
		return r.acquireAndRun(ctx, req, memoCP), true
	case group != nil:
		return r.acquireAndRun(ctx, req, concParamsFor(node, group, req.RunID)), true
	default:
		// Cache() was declared but produced no usable key (NoCache or
		// empty) and there is no group: run uncached on the normal path.
		return runner.Result{}, false
	}
}

// resolveCacheHash evaluates the node's content key, returning the hash
// (or "" when there is no Cache config, the key opted out via NoCache,
// or the key was empty) and the configured TTL.
func (r *InProcessRunner) resolveCacheHash(ctx context.Context, node *sparkwing.JobNode, cacheCfg *sparkwing.CacheConfig) (string, time.Duration) {
	if cacheCfg == nil {
		return "", 0
	}
	k := safeCacheKey(ctx, cacheCfg.Key, node.ID())
	switch {
	case k == sparkwing.NoCache:
		sparkwing.LoggerFromContext(ctx).Log("info",
			fmt.Sprintf("Cache(%s) returned NoCache; memoization explicitly skipped", node.ID()))
		return "", cacheCfg.TTL
	case k == "":
		sparkwing.LoggerFromContext(ctx).Log("warn",
			fmt.Sprintf("Cache(%s) returned empty CacheKey; memoization skipped (treating as missing key -- return sparkwing.NoCache to opt out explicitly)", node.ID()))
		return "", cacheCfg.TTL
	default:
		return string(k), cacheCfg.TTL
	}
}

// acquireAndRun performs one store acquire for cp and dispatches on the
// outcome: replay a hit, skip/fail under a full group, run a granted
// slot, or wait then run a queued/coalesced/evicting arrival.
func (r *InProcessRunner) acquireAndRun(ctx context.Context, req runner.Request, cp coordParams) runner.Result {
	node := req.Node
	holderID := fmt.Sprintf("%s/%s", req.RunID, node.ID())
	resp, err := r.backends.Concurrency.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key:           cp.key,
		HolderID:      holderID,
		RunID:         req.RunID,
		NodeID:        node.ID(),
		Capacity:      cp.capacity,
		Cost:          cp.cost,
		Policy:        cp.policy,
		CacheKeyHash:  cp.cacheHash,
		CacheTTL:      cp.cacheTTL,
		CancelTimeout: cp.cancelTimeout,
		BypassRead:    noCacheFromContext(ctx),
	})
	if err != nil {
		r.markFailed(ctx, req.RunID, node.ID(), fmt.Errorf("concurrency acquire(%q): %w", cp.key, err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}

	if resp.DriftNote != "" {
		payload, _ := json.Marshal(map[string]any{
			"key":               cp.key,
			"previous_capacity": resp.PreviousCapacity,
			"new_capacity":      cp.capacity,
			"note":              resp.DriftNote,
		})
		_ = r.backends.State.AppendEvent(ctx, req.RunID, node.ID(), "concurrency_drift", payload)
		slog.Default().Warn("concurrency drift", "key", cp.key, "prev", resp.PreviousCapacity, "new", cp.capacity)
	}

	switch resp.Kind {
	case store.AcquireCached:
		return r.applyCacheHit(ctx, req, cp, resp.OutputRef, resp.OriginRunID, resp.OriginNodeID)
	case store.AcquireSkipped:
		return r.applySkippedConcurrent(ctx, req)
	case store.AcquireFailed:
		err := fmt.Errorf("concurrency key %q slot full under OnLimit:Fail", cp.key)
		r.markFailed(ctx, req.RunID, node.ID(), err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	case store.AcquireGranted:
		return r.runHeldSlot(ctx, req, cp, holderID)
	case store.AcquireQueued, store.AcquireCoalesced, store.AcquireCancellingOthers:
		return r.waitThenRun(ctx, req, cp, resp)
	}

	err = fmt.Errorf("concurrency acquire returned unknown kind %q", resp.Kind)
	r.markFailed(ctx, req.RunID, node.ID(), err)
	return runner.Result{Outcome: sparkwing.Failed, Err: err}
}

// runMemoizedUnderConcurrency handles a node that declares both Cache
// and Concurrency. It first acquires the content-memo slot; a hit or an
// in-flight leader resolves without ever touching the group budget (so
// identical work draws one budget unit, not one per duplicate). The
// memo leader then competes for the group budget, runs, and on release
// writes the shared cache entry.
func (r *InProcessRunner) runMemoizedUnderConcurrency(ctx context.Context, req runner.Request, group *sparkwing.ConcurrencyGroup, cacheHash string, cacheTTL time.Duration) runner.Result {
	node := req.Node
	memoCP := coordParams{
		key:       memoKeyFor(cacheHash),
		capacity:  1,
		cost:      1,
		policy:    store.OnLimitCoalesce,
		cacheHash: cacheHash,
		cacheTTL:  cacheTTL,
	}
	memoHolderID := fmt.Sprintf("%s/%s", req.RunID, node.ID())
	resp, err := r.backends.Concurrency.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key:          memoCP.key,
		HolderID:     memoHolderID,
		RunID:        req.RunID,
		NodeID:       node.ID(),
		Capacity:     1,
		Cost:         1,
		Policy:       store.OnLimitCoalesce,
		CacheKeyHash: cacheHash,
		CacheTTL:     cacheTTL,
		BypassRead:   noCacheFromContext(ctx),
	})
	if err != nil {
		r.markFailed(ctx, req.RunID, node.ID(), fmt.Errorf("memo acquire(%q): %w", memoCP.key, err))
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}

	switch resp.Kind {
	case store.AcquireCached:
		return r.applyCacheHit(ctx, req, memoCP, resp.OutputRef, resp.OriginRunID, resp.OriginNodeID)
	case store.AcquireCoalesced:
		// A leader with identical content is already in flight; wait for
		// its result rather than competing for the group budget.
		return r.waitThenRun(ctx, req, memoCP, resp)
	case store.AcquireGranted:
		// Memo leader: keep the memo lease alive (so followers keep
		// waiting through our group wait + execution), run under the
		// group budget, then release the memo slot -- writing the shared
		// cache entry on success.
		execCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var lost atomic.Bool
		stopHB := r.startSlotHeartbeat(execCtx, memoCP.key, memoHolderID, &lost, cancel)

		result := r.acquireAndRun(execCtx, req, concParamsFor(node, group, req.RunID))

		stopHB()
		bg, bgCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer bgCancel()
		if err := r.backends.Concurrency.ReleaseSlot(bg, memoCP.key, memoHolderID,
			storeOutcome(result), fmt.Sprintf("%s/%s", req.RunID, node.ID()), cacheHash, cacheTTL); err != nil {
			slog.Warn("memo release failed; relying on reaper", "key", memoCP.key, "err", err)
		}
		return result
	default:
		err := fmt.Errorf("memo acquire(%q) returned unexpected kind %q", memoCP.key, resp.Kind)
		r.markFailed(ctx, req.RunID, node.ID(), err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
}

// storeOutcome maps a runner Result to the store's release-outcome
// string. Only "success" writes a cache entry on release.
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
	// so forward progress is bounded. A zero cancelTimeout relies on the
	// store's default eviction; the timer below only arms when the group
	// declared an explicit CancelTimeout.
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

	// queueTimeout bounds a queued waiter's wait. Zero = wait forever.
	// Only the Queue policy honors it.
	var queueDeadline time.Time
	if cp.queueTimeout > 0 && initial.Kind == store.AcquireQueued {
		queueDeadline = time.Now().Add(cp.queueTimeout)
	}

	// Give back the worker slot for the duration of the wait so a queue
	// of waiters can't starve other ready nodes. It is re-acquired
	// before execution on promotion; the paths that resolve without
	// executing (cache hit, leader-finished, cancel, timeout) leave it
	// released and runWithCap's deferred release no-ops.
	if req.ReleaseWorkerSlot != nil {
		req.ReleaseWorkerSlot()
	}

	// In-process only (cluster's HTTPConcurrency stubs ResolveWaiter).
	const pollInterval = 100 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Drop the parked waiter row so a later release can't promote
			// this cancelled node into a real holder (a phantom slot that
			// pins the budget until reaping). ctx is done, so cancel on a
			// fresh background context.
			bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if _, err := r.backends.Concurrency.CancelWaiter(bg, cp.key, req.RunID, req.Node.ID()); err != nil {
				slog.Warn("cancel waiter on context cancellation failed; reaper will sweep it",
					"key", cp.key, "run", req.RunID, "node", req.Node.ID(), "err", err)
			}
			cancel()
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
			// Re-take the worker slot before executing so the MaxParallel
			// cap is honored during the run, not just during the wait.
			if req.ReacquireWorkerSlot != nil && !req.ReacquireWorkerSlot() {
				r.markFailed(ctx, req.RunID, req.Node.ID(), context.Canceled)
				return runner.Result{Outcome: sparkwing.Cancelled}
			}
			_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "concurrency_promoted", nil)
			// Clear the "queued ..." detail now that the node holds a slot.
			_ = r.backends.State.UpdateNodeActivity(ctx, req.RunID, req.Node.ID(), "")
			return r.runHeldSlot(ctx, req, cp, res.HolderID)
		case store.WaiterCached:
			return r.applyCacheHit(ctx, req, cp, res.OutputRef, res.OriginRunID, res.OriginNodeID)
		case store.WaiterLeaderFinished:
			return r.inheritLeaderOutcome(ctx, req, cp, res.LeaderRunID, res.LeaderNodeID, res.LeaderOutcome)
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

// followerOutcomeFromLeader maps a coalesce leader's terminal node
// outcome to the outcome its dedupe followers inherit. A successful (or
// cached) leader lets followers replay Success; any non-success leader
// outcome is carried through so followers never go green for work that
// did not actually succeed. Unknown / empty outcomes fail safe.
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

// inheritLeaderOutcome adopts the leader's terminal node outcome +
// output when it finished without writing a cache entry. A leader that
// wrote no cache row did not succeed (only a successful release
// caches), so the follower must inherit the leader's actual node
// outcome -- a Skipped or Failed leader must not stamp the follower
// Success with empty output.
func (r *InProcessRunner) inheritLeaderOutcome(ctx context.Context, req runner.Request, cp coordParams, leaderRunID, leaderNodeID, leaderOutcome string) runner.Result {
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
		"leader_outcome": leaderOutcome,
	})
	_ = r.backends.State.AppendEvent(ctx, req.RunID, req.Node.ID(), "coalesced", payload)

	outcome := followerOutcomeFromLeader(leaderOutcome)
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
