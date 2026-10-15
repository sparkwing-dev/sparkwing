package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
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
	// Source reads the output and artifact manifest a hit or a coalesced
	// follower copies from the node runID/nodeID, in one read so both come
	// from the same node.
	Source func(ctx context.Context, in store.ClaimInputRequest, runID, nodeID string) (MemoSource, error)
	// NoCache runs every memoized node without reading an earlier result, as
	// --sw-no-cache does; a success still records its output.
	NoCache bool
}

// MemoSource is the finished node a hit or a coalesced follower copies.
type MemoSource struct {
	Output []byte
	// ArtifactManifest is the node's artifact manifest digest, empty when it
	// published none.
	ArtifactManifest string
}

// MemoSourceReader reads a finished node's row and output.
type MemoSourceReader interface {
	GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error)
	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)
}

// ReadMemoSource reads runID/nodeID's output and manifest from st. A finished
// node's row and output never change, so the two reads name one source. An
// output read's error is returned with the manifest.
func ReadMemoSource(ctx context.Context, st MemoSourceReader, runID, nodeID string) (MemoSource, error) {
	row, err := st.GetNode(ctx, runID, nodeID)
	if err != nil {
		return MemoSource{}, err
	}
	var src MemoSource
	if row != nil {
		src.ArtifactManifest = row.ArtifactManifest
	}
	src.Output, err = st.GetNodeOutput(ctx, runID, nodeID)
	return src, err
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
		if n.Plan.Memo == nil || n.Plan.Closures.MemoKey == "" {
			return next(ctx, n)
		}
		hash, res := memoHash(ctx, cfg, n)
		switch {
		case res.Outcome != sparkwing.Success:
			return res
		case hash == "":
			return next(ctx, n)
		case m.Slots == nil || m.Source == nil:
			return Result{Outcome: sparkwing.Failed, Err: errors.New("memo: no coordination store")}
		}
		g := group{key: memoKeyPrefix + hash, policy: store.OnLimitCoalesce, capacity: 1, cost: 1, memoHash: hash}
		if memo := n.Plan.Memo; memo != nil {
			g.memoTTL = millis(value(memo.TTLMS))
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
	id := n.Plan.Closures.MemoKey
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
		if err := er.ValidateValue("memo_key"); err != nil {
			return keyFailed(err)
		}
		if len(er.Value) == 0 || string(er.Value) == "null" {
			closureNote(log, LevelInfo, fmt.Sprintf("Memoize(%s) returned NoCache; memoization skipped", n.Plan.ID))
			return Result{Outcome: sparkwing.Success}
		}
		if err := json.Unmarshal(er.Value, &hash); err != nil {
			return keyFailed(hostError(apierr.ProtocolError, "cache_key %s returned %s, not a string or null", id, er.Value))
		}
		if hash == "" {
			return keyFailed(errors.New("empty key"))
		}
		return Result{Outcome: sparkwing.Success}
	})
	return hash, res
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
	in := store.ClaimInputRequest{Kind: store.ClaimInputCached, Key: r.g.key, CacheKeyHash: r.g.memoHash}
	src, err := r.m.Source(ctx, in, srcRun, srcNode)
	switch {
	case (errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrOutputCorrupt)) && !bypass:
		return r.run(ctx, true)
	case err != nil:
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("cache hit: fetch output: %w", err)}, false
	}
	switch ready, err := r.artifactsReady(ctx, src.ArtifactManifest); {
	case err != nil:
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("cache hit: read artifacts: %w", err)}, false
	case !ready:
		return r.run(ctx, true)
	}
	r.copied(ctx, src.ArtifactManifest, sparkwing.Memoized, "cache_hit", map[string]any{
		"key": r.g.key, "cache_key_hash": r.g.memoHash, "origin_run_id": srcRun, "origin_node_id": srcNode,
	}, map[string]any{"cache_hit": true})
	return Result{Outcome: sparkwing.Memoized, Output: src.Output}, true
}

// safety: the store resolves a follower this way only once its leader
// succeeded, so the follower succeeds with the leader's output.
func (r *memoRun) follow(ctx context.Context, leaderRun, leaderNode string) (Result, bool) {
	in := store.ClaimInputRequest{Kind: store.ClaimInputCoalesced, Key: r.g.key, CacheKeyHash: r.g.memoHash}
	src, err := r.m.Source(ctx, in, leaderRun, leaderNode)
	if errors.Is(err, store.ErrNotFound) {
		// safety: a leader may lose its output before a follower reads it; missing bytes must run as a miss.
		return r.run(ctx, true)
	}
	if err != nil {
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("fetch leader output: %w", err)}, false
	}
	switch ready, err := r.artifactsReady(ctx, src.ArtifactManifest); {
	case err != nil:
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("read leader artifacts: %w", err)}, false
	case !ready:
		return r.run(ctx, true)
	}
	r.copied(ctx, src.ArtifactManifest, sparkwing.Success, "coalesced", map[string]any{
		"key": r.g.key, "leader_run_id": leaderRun, "leader_node_id": leaderNode, "leader_outcome": string(sparkwing.Success),
	}, map[string]any{"coalesced_from": leaderRun + "/" + leaderNode})
	return Result{Outcome: sparkwing.Success, Output: src.Output}, true
}

// safety: a node that declares outputs reuses a source only when its manifest and every blob it lists are
// stored, since a consumer of a missing one fails where the producer could have run again. A host with no
// artifact store publishes and stages nothing, so there is nothing to check.
func (r *memoRun) artifactsReady(ctx context.Context, manifest string) (bool, error) {
	switch {
	case len(r.n.Plan.Artifacts) == 0:
		return true, nil
	case manifest == "":
		return false, nil
	case r.cfg.Artifacts == nil:
		return true, nil
	}
	return artifacts.Present(ctx, r.cfg.Artifacts, manifest)
}

// safety: the event kinds, payloads and log attrs are the dispatcher's, which the dashboard's live stream reads.
func (r *memoRun) copied(ctx context.Context, manifest string, outcome sparkwing.Outcome, kind string, event, attrs map[string]any) {
	run, id := r.n.Run, r.n.Plan.ID
	wctx := context.WithoutCancel(ctx)
	if err := run.State.StartNode(wctx, run.ID, id); err != nil {
		run.Logger.Warn("nodehost: start copied node", "node", id, "err", err)
	}
	hostEventJSON(wctx, run, id, kind, event)
	if manifest != "" {
		if err := run.State.SetNodeArtifactManifest(wctx, run.ID, id, manifest); err != nil {
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
	if len(c.Generate) == 0 {
		return nil
	}
	held := *n
	held.memoOutput = output
	if held.memoOutput == nil {
		held.memoOutput = json.RawMessage("null")
	}
	var out []expansion
	// safety: the eval runs the node's own code, so it is held to the node's timeout like an attempt; a hung
	// generator would otherwise hold the scheduler until the run itself ends.
	res := timeoutLayer(cfg, func(ctx context.Context, attempt *NodeRun) Result {
		return cfg.Attempts.process(ctx, cfg.Session, attempt, func(ctx context.Context, sess *Session, _ *LogSink) Result {
			if err := nodeReplan(ctx, sess, attempt); err != nil {
				return failed(err)
			}
			out = generate(ctx, sess, attempt)
			// safety: a generator cut off by the timeout reports a failure, which the timeout layer names.
			if err := ctx.Err(); err != nil {
				return failed(err)
			}
			return Result{Outcome: sparkwing.Success}
		})
	})(ctx, &held)
	if res.Outcome != sparkwing.Success {
		out = out[:0]
		for _, id := range c.Generate {
			out = append(out, expansion{closure: id, err: res.Err})
		}
	}
	return out
}
