package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Memo reuses a memoized node's earlier result the way the in-process
// dispatcher does. The node's cache_key closure names the result; the
// coordination store finds a stored one, or coalesces concurrent runs of
// the same key behind one leader; and a success records its output.
type Memo struct {
	// Slots is the coordination store that holds the memo entries.
	Slots ConcurrencySlots
	// Output reads the output a hit or a coalesced follower copies from the
	// node runID/nodeID. A claimed node cannot read another run, so it names
	// the entry with in and the controller picks the node.
	Output func(ctx context.Context, in store.ClaimInputRequest, runID, nodeID string) ([]byte, error)
	// NoCache runs every memoized node without reading an earlier result, as
	// --sw-no-cache does; a success still records its output.
	NoCache bool
	// Claimed keys each entry under the run's repository, pipeline and node,
	// the only memo keys the controller lets a claim take.
	Claimed bool
}

// safety: matches the dispatcher's bound on a cache key resolver.
const memoKeyBudget = 30 * time.Second

const memoKeyPrefix = "memo:"

// safety: the cache_key closure runs in an eval process of its own before the
// node starts, so a node whose result is reused never gets an attempt. A
// custom executor owns the whole node, so without Attempts the layer is off.
func memoLayer(cfg *Config, next Executor) Executor {
	m := cfg.Memo
	if m == nil || cfg.Attempts == nil {
		return next
	}
	return func(ctx context.Context, n *NodeRun) Result {
		if n.Plan.MemoKey == nil || n.Plan.MemoKey.Closure == "" {
			return next(ctx, n)
		}
		hash, res := memoHash(ctx, cfg, n)
		switch {
		case res.Outcome != sparkwing.Success:
			return res
		case hash == "":
			return next(ctx, n)
		case m.Slots == nil || m.Output == nil:
			return Result{Outcome: sparkwing.Failed, Err: errors.New("memo: no coordination store")}
		}
		key, err := m.key(ctx, n, hash)
		if err != nil {
			return Result{Outcome: sparkwing.Failed, Err: err}
		}
		g := group{key: key, policy: store.OnLimitCoalesce, capacity: 1, cost: 1, memoHash: hash}
		if mod := n.Plan.Modifiers; mod != nil {
			g.memoTTL = time.Duration(mod.CacheTTLMS) * time.Millisecond
		}
		mr := &memoRun{cfg: cfg, m: m, adm: &Admission{Slots: m.Slots}, n: n, next: next, g: g}
		res, copied := mr.run(ctx, m.NoCache)
		if copied {
			res.expansions = memoGenerate(ctx, cfg, n, res.Output)
		}
		return res
	}
}

// safety: an empty hash with a success is the closure's null, so the node runs without memo.
func memoHash(ctx context.Context, cfg *Config, n *NodeRun) (string, Result) {
	id := n.Plan.MemoKey.Closure
	hash := ""
	res := cfg.Attempts.process(ctx, cfg.Session, n, func(ctx context.Context, sess *Session, log *LogSink) Result {
		keyFailed := func(err error) Result {
			return failed(fmt.Errorf("resolve cache key for %s: %w", n.Plan.ID, err))
		}
		if err := nodeReplan(ctx, sess, n); err != nil {
			return failed(err)
		}
		kctx, cancel := context.WithTimeout(ctx, memoKeyBudget)
		defer cancel()
		var er nodeproto.EvalResult
		if err := ask(kctx, sess, nodeproto.Request{Op: nodeproto.OpEval, Closure: id}, &er); err != nil {
			return keyFailed(err)
		}
		if er.Error != nil {
			return keyFailed(errors.New(*er.Error))
		}
		if len(er.Value) == 0 || string(er.Value) == "null" {
			closureNote(log, LevelInfo, fmt.Sprintf("Memoize(%s) returned NoCache; memoization skipped", n.Plan.ID))
			return Result{Outcome: sparkwing.Success}
		}
		if err := json.Unmarshal(er.Value, &hash); err != nil {
			return keyFailed(hostError(nodeproto.CodeProtocolError, "cache_key %s returned %s, not a string or null", id, er.Value))
		}
		if hash == "" {
			return keyFailed(errors.New("empty key"))
		}
		return Result{Outcome: sparkwing.Success}
	})
	return hash, res
}

// safety: a claimed node memoizes under its own repository, pipeline and node,
// read from the run row as the controller reads it; elsewhere a key is shared
// across pipelines, as in the dispatcher.
func (m *Memo) key(ctx context.Context, n *NodeRun, hash string) (string, error) {
	if !m.Claimed {
		return memoKeyPrefix + hash, nil
	}
	run, err := n.Run.State.GetRun(ctx, n.Run.ID)
	if err != nil {
		return "", fmt.Errorf("read the run to name its memo key: %w", err)
	}
	return store.ClaimMemoKey(store.ClaimRepoSlug(run.GithubOwner, run.GithubRepo), run.Pipeline, n.Plan.ID, hash), nil
}

type memoRun struct {
	cfg  *Config
	m    *Memo
	adm  *Admission
	n    *NodeRun
	next Executor
	g    group
}

func (r *memoRun) run(ctx context.Context, bypass bool) (res Result, copied bool) {
	n, g := r.n, r.g
	resp, err := r.m.Slots.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key: g.key, HolderID: n.Run.ID + "/" + n.Plan.ID, RunID: n.Run.ID, NodeID: n.Plan.ID,
		Capacity: g.capacity, Cost: g.cost, Policy: g.policy, CacheKeyHash: g.memoHash, CacheTTL: g.memoTTL, BypassRead: bypass,
	})
	if err != nil {
		return admissionFailed(ctx, fmt.Errorf("memo acquire(%q): %w", g.key, err)), false
	}
	switch resp.Kind {
	case store.AcquireCached:
		return r.hit(ctx, resp.OriginRunID, resp.OriginNodeID, bypass)
	case store.AcquireGranted:
		return r.hold(ctx, n.Run.ID+"/"+n.Plan.ID), false
	case store.AcquireCoalesced, store.AcquireQueued:
		return r.wait(ctx, resp, bypass)
	}
	return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("memo acquire(%q) returned unexpected kind %q", g.key, resp.Kind)}, false
}

// safety: the row is written before the slot's release records the entry, so
// a follower or a later run never reads the entry's origin before its output.
func (r *memoRun) hold(ctx context.Context, holder string) Result {
	n := r.n
	return r.adm.holdSlot(ctx, r.adm.nodeHolder(ctx, n), r.g, holder, func(ctx context.Context) Result {
		res := r.next(ctx, n)
		if res.Outcome != sparkwing.Success || res.recorded {
			return res
		}
		err := n.Run.State.FinishNodeWithReason(context.WithoutCancel(ctx), n.Run.ID, n.Plan.ID, string(res.Outcome), "", res.Output, res.Reason, nil)
		if err != nil {
			return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("record node result: %w", err)}
		}
		res.recorded = true
		return res
	})
}

func (r *memoRun) wait(ctx context.Context, initial store.AcquireSlotResponse, bypass bool) (Result, bool) {
	n, g, slots := r.n, r.g, r.m.Slots
	h := r.adm.nodeHolder(ctx, n)
	h.activity(fmt.Sprintf("coalescing in %s behind %s", g.key, initial.LeaderRunID+"/"+initial.LeaderNodeID))
	cancelWaiter := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := slots.CancelWaiter(cctx, g.key, n.Run.ID, n.Plan.ID); err != nil {
			h.log.Warn("nodehost: cancel memo waiter; the reaper sweeps it", "key", g.key, "err", err)
		}
	}
	tick := time.NewTicker(waiterPoll)
	defer tick.Stop()
	var failingSince time.Time
	for {
		select {
		case <-ctx.Done():
			cancelWaiter()
			return admissionFailed(ctx, ctx.Err()), false
		case <-tick.C:
		}
		res, err := slots.ResolveWaiter(ctx, g.key, n.Run.ID, n.Plan.ID, g.memoHash, initial.LeaderRunID, initial.LeaderNodeID, bypass)
		if err != nil {
			if failingSince.IsZero() {
				failingSince = outageClock()
			}
			if store.IsProtocolErr(err) || outageClock().Sub(failingSince) >= storeOutageBudget {
				cancelWaiter()
				return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("memo key %q: resolve waiter: %w", g.key, err)}, false
			}
			continue
		}
		failingSince = time.Time{}
		if res.Status != store.WaiterStillWaiting {
			h.activity("")
		}
		switch res.Status {
		case store.WaiterStillWaiting:
		case store.WaiterPromoted:
			return r.hold(ctx, res.HolderID), false
		case store.WaiterCached:
			return r.hit(ctx, res.OriginRunID, res.OriginNodeID, bypass)
		case store.WaiterLeaderFinished:
			return r.follow(ctx, res.LeaderRunID, res.LeaderNodeID)
		case store.WaiterCancelled:
			return Result{Outcome: sparkwing.Superseded, Err: fmt.Errorf("memo key %q: waiter was cancelled or superseded", g.key)}, false
		default:
			cancelWaiter()
			return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("memo key %q: unexpected waiter status %q", g.key, res.Status)}, false
		}
	}
}

// safety: an entry whose artifacts or output bytes are gone, or whose bytes
// fail their digest, runs as a miss rather than hand on a wrong value; the
// rerun skips reading entries, so it cannot loop.
func (r *memoRun) hit(ctx context.Context, srcRun, srcNode string, bypass bool) (Result, bool) {
	n := r.n
	if len(n.Plan.Outputs) > 0 {
		// bug: a claim cannot read another run's node yet, so a claimed hit with artifacts runs as a miss.
		if r.m.Claimed {
			return r.run(ctx, true)
		}
		src, err := n.Run.State.GetNode(ctx, srcRun, srcNode)
		switch {
		case errors.Is(err, store.ErrNotFound) || (err == nil && (src == nil || src.ArtifactManifest == "")):
			return r.run(ctx, true)
		case err != nil:
			return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("cache hit: read artifacts: %w", err)}, false
		}
	}
	in := store.ClaimInputRequest{Kind: store.ClaimInputCached, Key: r.g.key, CacheKeyHash: r.g.memoHash}
	output, err := r.m.Output(ctx, in, srcRun, srcNode)
	switch {
	case (errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrOutputCorrupt)) && !bypass:
		return r.run(ctx, true)
	case err != nil:
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("cache hit: fetch output: %w", err)}, false
	}
	r.copied(ctx, srcRun, srcNode, sparkwing.Cached, "cache_hit", map[string]any{
		"key": r.g.key, "cache_key_hash": r.g.memoHash, "origin_run_id": srcRun, "origin_node_id": srcNode,
	}, map[string]any{"cache_hit": true})
	return Result{Outcome: sparkwing.Cached, Output: output}, true
}

// safety: the store resolves a follower this way only once its leader
// succeeded, so the follower succeeds with the leader's output.
func (r *memoRun) follow(ctx context.Context, leaderRun, leaderNode string) (Result, bool) {
	in := store.ClaimInputRequest{Kind: store.ClaimInputCoalesced, Key: r.g.key, CacheKeyHash: r.g.memoHash}
	output, err := r.m.Output(ctx, in, leaderRun, leaderNode)
	if errors.Is(err, store.ErrNotFound) && r.m.Claimed {
		// safety: a claim finds no output once the entry expired after its leader released, so the node runs
		// rather than succeed with nothing.
		return r.run(ctx, true)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("fetch leader output: %w", err)}, false
	}
	r.copied(ctx, leaderRun, leaderNode, sparkwing.Success, "coalesced", map[string]any{
		"key": r.g.key, "leader_run_id": leaderRun, "leader_node_id": leaderNode, "leader_outcome": string(sparkwing.Success),
	}, map[string]any{"coalesced_from": leaderRun + "/" + leaderNode})
	return Result{Outcome: sparkwing.Success, Output: output}, true
}

// safety: the event kinds, payloads and log attrs are the dispatcher's, which the dashboard's live stream reads.
func (r *memoRun) copied(ctx context.Context, srcRun, srcNode string, outcome sparkwing.Outcome, kind string, event, attrs map[string]any) {
	run, id := r.n.Run, r.n.Plan.ID
	wctx := context.WithoutCancel(ctx)
	if err := run.State.StartNode(wctx, run.ID, id); err != nil {
		run.Logger.Warn("nodehost: start copied node", "node", id, "err", err)
	}
	hostEventJSON(wctx, run, id, kind, event)
	if src, err := run.State.GetNode(wctx, srcRun, srcNode); err == nil && src != nil && src.ArtifactManifest != "" {
		if err := run.State.SetNodeArtifactManifest(wctx, run.ID, id, src.ArtifactManifest); err != nil {
			run.Logger.Warn("nodehost: copy artifact manifest", "node", id, "err", err)
		}
	}
	attrs["outcome"], attrs["duration_ms"] = string(outcome), int64(0)
	hostNodeLog(wctx, r.cfg, r.n, sparkwing.LogRecord{Level: LevelInfo, Event: "node_end", Attrs: attrs})
}

// safety: a copied source never ran in a process, so its generators run in an
// eval process whose output route serves the copied output as the source's
// own; a generated node rereads it from the source's row as usual.
func memoGenerate(ctx context.Context, cfg *Config, n *NodeRun, output json.RawMessage) []expansion {
	c := n.Plan.Closures
	if c == nil || len(c.Generator) == 0 {
		return nil
	}
	held := *n
	held.memoOutput = output
	if held.memoOutput == nil {
		held.memoOutput = json.RawMessage("null")
	}
	var out []expansion
	res := cfg.Attempts.process(ctx, cfg.Session, &held, func(ctx context.Context, sess *Session, _ *LogSink) Result {
		if err := nodeReplan(ctx, sess, &held); err != nil {
			return failed(err)
		}
		out = generate(ctx, sess, &held)
		return Result{Outcome: sparkwing.Success}
	})
	if res.Outcome != sparkwing.Success {
		out = out[:0]
		for _, id := range c.Generator {
			out = append(out, expansion{closure: id, err: res.Err})
		}
	}
	return out
}
