package nodehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
	// NoCache runs every memoized node without reading an earlier result, as
	// --sw-no-cache does; a success still records its output.
	NoCache bool
}

// safety: matches the dispatcher's bound on a cache key resolver.
const memoKeyBudget = 30 * time.Second

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
		if cfg.Artifacts == nil {
			return next(ctx, n)
		}
		hash, res := memoHash(ctx, cfg, n)
		switch {
		case res.Outcome != sparkwing.Success:
			return res
		case hash == "":
			return next(ctx, n)
		case m.Slots == nil:
			return Result{Outcome: sparkwing.Failed, Err: errors.New("memo: no coordination store")}
		}
		data, err := cfg.Artifacts(ctx, n)
		if err != nil {
			return Result{Outcome: sparkwing.Failed, Err: err}
		}
		hash = fmt.Sprintf("%x", sha256.Sum256([]byte(hash)))
		key, err := blob.MemoNamespace(blob.MemoKey{Repo: data.Authority.Repo, Ref: data.Authority.Ref.Scope, Pipeline: data.Pipeline, Job: n.Plan.ID, Hash: runspec.Hash(hash)})
		if err != nil {
			return Result{Outcome: sparkwing.Failed, Err: err}
		}
		g := group{key: key, policy: store.OnLimitCoalesce, capacity: 1, cost: 1, memoHash: hash}
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
	if !bypass && !r.m.NoCache {
		return r.materialize(ctx)
	}
	n, g := r.n, r.g
	resp, err := r.m.Slots.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key: g.key, HolderID: n.Run.ID + "/" + n.Plan.ID, RunID: n.Run.ID, NodeID: n.Plan.ID,
		Capacity: g.capacity, Cost: g.cost, Policy: g.policy, BypassRead: r.m.NoCache,
	})
	if err != nil {
		return admissionFailed(ctx, fmt.Errorf("memo acquire(%q): %w", g.key, err)), false
	}
	switch resp.Kind {
	case store.AcquireGranted:
		return r.hold(ctx, n.Run.ID+"/"+n.Plan.ID)
	case store.AcquireCoalesced, store.AcquireQueued:
		return r.wait(ctx, resp)
	}
	return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("memo acquire(%q) returned unexpected kind %q", g.key, resp.Kind)}, false
}

// safety: the slot only coordinates execution; blob records own every memo edge.
func (r *memoRun) hold(ctx context.Context, holder string) (Result, bool) {
	n := r.n
	coordination := r.g
	coordination.memoHash, coordination.memoTTL = "", 0
	copied := false
	result := r.adm.holdSlot(ctx, r.adm.nodeHolder(ctx, n), coordination, holder, func(ctx context.Context) Result {
		if !r.m.NoCache {
			res, found, err := r.lookup(ctx)
			if err != nil {
				return admissionFailed(ctx, err)
			}
			if found {
				copied = true
				return res
			}
		}
		res := r.next(ctx, n)
		if res.Outcome != sparkwing.Success || res.recorded {
			return res
		}
		if err := r.save(ctx, res.Output); err != nil {
			n.Run.Logger.Warn("memo write failed; result remains successful", "node", n.Plan.ID, "err", err)
		}
		err := n.Run.State.FinishNodeWithReason(context.WithoutCancel(ctx), n.Run.ID, n.Plan.ID, string(res.Outcome), "", res.Output, res.Reason, nil)
		if err != nil {
			return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("record node result: %w", err)}
		}
		res.recorded = true
		return res
	})
	return result, copied && result.Outcome == sparkwing.Memoized
}

func (r *memoRun) wait(ctx context.Context, initial store.AcquireSlotResponse) (Result, bool) {
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
		res, err := slots.ResolveWaiter(ctx, g.key, n.Run.ID, n.Plan.ID, "", initial.LeaderRunID, initial.LeaderNodeID, r.m.NoCache)
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
			return r.hold(ctx, res.HolderID)
		case store.WaiterCached:
			return r.materialize(ctx)
		case store.WaiterLeaderFinished:
			return r.materialize(ctx)
		case store.WaiterCancelled:
			return Result{Outcome: sparkwing.Superseded, Err: fmt.Errorf("memo key %q: waiter was cancelled or superseded", g.key)}, false
		default:
			cancelWaiter()
			return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("memo key %q: unexpected waiter status %q", g.key, res.Status)}, false
		}
	}
}

// safety: result edges are copied atomically; missing or unreadable bytes rerun
// the job instead of handing a follower a successful empty result.
func (r *memoRun) materialize(ctx context.Context) (Result, bool) {
	result, found, err := r.lookup(ctx)
	if err != nil {
		return admissionFailed(ctx, err), false
	}
	if found {
		return result, true
	}
	return r.run(ctx, true)
}

func (r *memoRun) lookup(ctx context.Context) (Result, bool, error) {
	if r.cfg.Artifacts == nil {
		return Result{}, false, nil
	}
	data, err := r.cfg.Artifacts(ctx, r.n)
	if err != nil {
		return Result{}, false, err
	}
	if !data.Identity.Attempt.Set {
		return Result{}, false, errors.New("memo requires an attempt binding")
	}
	copied, err := blob.MaterializeMemo(ctx, data.Bytes, data.Records, blob.CopyMemoRequest{
		Lookup: blob.MemoLookup{Authority: data.Authority, Budget: data.Budget, Key: blob.MemoKey{
			Repo: data.Authority.Repo, Ref: data.Authority.Ref.Scope, Pipeline: data.Pipeline,
			Job: r.n.Plan.ID, Hash: runspec.Hash(r.g.memoHash),
		}},
		Destination: data.Identity.Attempt.Value,
	})
	if err != nil {
		return Result{}, false, err
	}
	if !copied.Hit.Found || (len(r.n.Plan.Artifacts) > 0 && !copied.Hit.Manifest.Set) {
		return Result{}, false, nil
	}
	if err := r.n.Run.State.StartNode(ctx, r.n.Run.ID, r.n.Plan.ID); err != nil {
		return Result{}, false, fmt.Errorf("start memoized job: %w", err)
	}
	return Result{Outcome: sparkwing.Memoized, Output: copied.Output}, true, nil
}

func (r *memoRun) save(ctx context.Context, output []byte) error {
	if r.cfg.Artifacts == nil {
		return nil
	}
	data, err := r.cfg.Artifacts(ctx, r.n)
	if err != nil {
		return err
	}
	if data.Bytes == nil || data.Records == nil {
		return errors.New("memo requires records and bytes")
	}
	if int64(len(output)) > limits.NodeOutputBytes {
		return fmt.Errorf("memo output exceeds %d bytes", limits.NodeOutputBytes)
	}
	key, err := blob.ResolveWriteKey(data.Authority, blob.WriteScopeIntent{Kind: blob.KindMemo, Pipeline: data.Pipeline, Job: r.n.Plan.ID, Key: r.g.memoHash, Name: blob.MemoRecordName})
	if err != nil {
		return err
	}
	request := blob.CommitRecordRequest{Source: data.Identity, Authority: data.Authority, Key: key}
	if len(r.n.Plan.Artifacts) > 0 {
		manifest, err := data.Records.LookupRecord(ctx, blob.RecordLookup{Authority: data.Authority, Budget: data.Budget, Key: blob.RecordKey{
			Team: data.Identity.Team, Kind: blob.KindManifest, Name: data.Identity.RunID,
			Scope: blob.Scope{Repo: data.Authority.Repo, Ref: data.Authority.Ref.Scope, Pipeline: data.Pipeline, Job: r.n.Plan.ID},
		}})
		if err != nil {
			return err
		}
		if !manifest.Found {
			return blob.ErrNotFound
		}
		request.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: manifest.Record.Digest}
		request.Members = manifest.Members
	}
	request.Digest = blob.Digest(fmt.Sprintf("%x", sha256.Sum256(output)))
	if _, err := data.Bytes.Put(ctx, blob.PutRequest{Authority: data.Authority, Identity: data.Identity, Digest: request.Digest, Size: int64(len(output)), Body: bytes.NewReader(output)}); err != nil {
		return err
	}
	if r.g.memoTTL > 0 {
		request.ExpiresAt = runspec.Optional[time.Time]{Set: true, Value: time.Now().Add(r.g.memoTTL)}
	}
	_, err = data.Records.CommitRecord(ctx, request)
	return err
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
