package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// s3CASMaxRetries bounds the read-modify-PutIfMatch loop. Contended
// keys serialize through the loop one winner per round, so the bound
// must exceed the realistic number of simultaneous contenders; beyond
// it the caller sees a transient error and the orchestrator's release
// reaper recovers the slot.
const s3CASMaxRetries = 200

// s3CASBackoffStep and s3CASBackoffCap shape the per-attempt sleep
// between lost CAS rounds. A holder-derived offset spreads contenders
// so they don't re-collide in lockstep.
const (
	s3CASBackoffStep = 2 * time.Millisecond
	s3CASBackoffCap  = 40 * time.Millisecond
)

// s3FinishedRetention bounds how long a drained coalesce leader's
// terminal outcome lingers in the slot so a slow follower can still
// inherit it. Pruned on the next write past the window.
const s3FinishedRetention = 5 * time.Minute

// s3Concurrency is the Mode 2 (object-store) ConcurrencyBackend. It
// holds the full coordination state for each concurrency key in a
// single versioned slot object and mutates it under conditional-write
// CAS, so N runners against one bucket coalesce on a key instead of
// every slot being granted unconditionally.
//
// Every mutation is a read-modify-PutIfMatch retry loop against one
// object per key. This is the deliberate tradeoff: a heavily-contended
// key serializes its acquires/releases as retries against a single
// object, so its tail latency is higher than a database's row lock.
// Uncontended keys touch the object once.
//
// When the configured endpoint does not enforce write preconditions
// (ConditionalWritesSupported reports false), the backend falls back to
// noopConcurrency behavior -- granting every slot with a warning --
// rather than handing out unsafe locks against an endpoint that ignores
// the CAS.
type s3Concurrency struct {
	cw storage.ConditionalWriter

	probeOnce sync.Once
	useNoop   bool
	noop      *noopConcurrency
}

// NewS3Concurrency returns a cross-runner ConcurrencyBackend over the
// artifact store's conditional-write capability. When the store does
// not implement ConditionalWriter (or art is nil), it returns the
// no-op backend directly. When it does, the live-endpoint capability is
// probed lazily on first use; an endpoint that ignores preconditions
// degrades to no-op coordination.
func NewS3Concurrency(art storage.ArtifactStore) ConcurrencyBackend {
	cw, ok := storage.Conditional(art)
	if !ok {
		return &noopConcurrency{}
	}
	return &s3Concurrency{cw: cw, noop: &noopConcurrency{}}
}

// fallback returns the no-op backend when conditional writes are
// unavailable, or nil when the CAS path is usable. The capability probe
// runs once.
func (c *s3Concurrency) fallback(ctx context.Context) ConcurrencyBackend {
	c.probeOnce.Do(func() {
		ok, err := c.cw.ConditionalWritesSupported(ctx)
		switch {
		case err != nil:
			c.useNoop = true
			slog.Warn("conditional-write probe failed; cache concurrency falls back to no-op "+
				"(no cross-runner reservation in this state backend)", "err", err)
		case !ok:
			c.useNoop = true
			slog.Warn("object store ignores write preconditions; cache concurrency falls back to no-op " +
				"(no cross-runner reservation in this state backend)")
		}
	})
	if c.useNoop {
		return c.noop
	}
	return nil
}

// s3SlotDoc is the full coordination state for one concurrency key,
// serialized as one object. Holders draw budget; waiters await it;
// cache memoizes a finished leader's output; finished records carry a
// drained coalesce leader's outcome to its followers.
type s3SlotDoc struct {
	Key      string             `json:"key"`
	Capacity int                `json:"capacity"`
	Holders  []s3Holder         `json:"holders,omitempty"`
	Waiters  []s3Waiter         `json:"waiters,omitempty"`
	Cache    map[string]s3Cache `json:"cache,omitempty"`
	Finished []s3FinishedHolder `json:"finished,omitempty"`
}

type s3Holder struct {
	HolderID         string `json:"holder_id"`
	RunID            string `json:"run_id"`
	NodeID           string `json:"node_id"`
	ClaimedAtNS      int64  `json:"claimed_at_ns"`
	LeaseExpiresNS   int64  `json:"lease_expires_ns"`
	Superseded       bool   `json:"superseded,omitempty"`
	Cost             int    `json:"cost"`
	DeclaredCapacity int    `json:"declared_capacity"`
}

const inheritedS3HolderDeclaredCapacity = -1
const inheritedS3HolderNodePrefix = "\x00inherited:"

func inheritedS3HolderNodeID(holderID string) string {
	return inheritedS3HolderNodePrefix + holderID
}

func isInheritedS3HolderNodeID(nodeID string) bool {
	return strings.HasPrefix(nodeID, inheritedS3HolderNodePrefix)
}

func (h s3Holder) budgetCost() int {
	if h.Cost == 0 && h.DeclaredCapacity == inheritedS3HolderDeclaredCapacity {
		return 0
	}
	if h.Cost <= 0 {
		return 1
	}
	return h.Cost
}

type s3Waiter struct {
	RunID            string `json:"run_id"`
	NodeID           string `json:"node_id"`
	HolderID         string `json:"holder_id"`
	ArrivedAtNS      int64  `json:"arrived_at_ns"`
	Policy           string `json:"policy"`
	CacheKeyHash     string `json:"cache_key_hash,omitempty"`
	LeaderRunID      string `json:"leader_run_id,omitempty"`
	LeaderNodeID     string `json:"leader_node_id,omitempty"`
	Cost             int    `json:"cost"`
	DeclaredCapacity int    `json:"declared_capacity"`
}

type s3Cache struct {
	OutputRef    string `json:"output_ref"`
	OriginRunID  string `json:"origin_run_id"`
	OriginNodeID string `json:"origin_node_id"`
	CreatedNS    int64  `json:"created_ns"`
	ExpiresNS    int64  `json:"expires_ns"`
}

// s3FinishedHolder records a drained coalesce leader's terminal outcome
// so a follower that resolves after the drain inherits it (the cache
// path covers a successful leader; this covers a leader that wrote no
// cache entry).
type s3FinishedHolder struct {
	RunID      string `json:"run_id"`
	NodeID     string `json:"node_id"`
	Outcome    string `json:"outcome"`
	RecordedNS int64  `json:"recorded_ns"`
}

// s3SlotKey maps an arbitrary coordination key (which may contain any
// byte) to a stable, safe object key.
func s3SlotKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "concurrency/" + hex.EncodeToString(sum[:]) + ".json"
}

func nodeOrDash(nodeID string) string {
	if nodeID == "" {
		return "-"
	}
	return nodeID
}

// load reads and decodes the slot object. A missing object yields a
// fresh empty doc with exists=false so the first writer uses
// PutIfAbsent.
func (c *s3Concurrency) load(ctx context.Context, objKey string) (*s3SlotDoc, storage.ETag, bool, error) {
	rc, etag, err := c.cw.GetWithETag(ctx, objKey)
	if errors.Is(err, storage.ErrNotFound) {
		return &s3SlotDoc{Cache: map[string]s3Cache{}}, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", false, err
	}
	var doc s3SlotDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, "", false, fmt.Errorf("decode concurrency slot %s: %w", objKey, err)
	}
	if doc.Cache == nil {
		doc.Cache = map[string]s3Cache{}
	}
	return &doc, etag, true, nil
}

// mutate runs fn against the current slot state and, when fn requests a
// write, commits it under CAS. A lost precondition re-reads and retries.
// fn returns (write, err): a returned error aborts without writing and
// propagates verbatim so sentinels survive errors.Is.
func (c *s3Concurrency) mutate(ctx context.Context, key string, fn func(doc *s3SlotDoc, exists bool, now time.Time) (bool, error)) error {
	objKey := s3SlotKey(key)
	for attempt := 0; ; attempt++ {
		doc, etag, exists, err := c.load(ctx, objKey)
		if err != nil {
			return err
		}
		write, err := fn(doc, exists, time.Now())
		if err != nil {
			return err
		}
		if !write {
			return nil
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if exists {
			_, err = c.cw.PutIfMatch(ctx, objKey, bytes.NewReader(body), etag)
		} else {
			_, err = c.cw.PutIfAbsent(ctx, objKey, bytes.NewReader(body))
		}
		if err == nil {
			return nil
		}
		if !errors.Is(err, storage.ErrPreconditionFailed) {
			return err
		}
		if attempt >= s3CASMaxRetries {
			return fmt.Errorf("concurrency CAS on %q exhausted %d retries: %w", key, s3CASMaxRetries, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(casBackoff(attempt, key)):
		}
	}
}

// casBackoff grows the inter-attempt sleep linearly to the cap, offset
// by a key-derived jitter so contenders on one slot don't re-collide in
// lockstep round after round.
func casBackoff(attempt int, key string) time.Duration {
	d := time.Duration(attempt+1) * s3CASBackoffStep
	if d > s3CASBackoffCap {
		d = s3CASBackoffCap
	}
	jitter := time.Duration(sha256.Sum256([]byte(key))[0]) % s3CASBackoffStep
	return d + jitter
}

func liveHolderCost(holders []s3Holder, nowNS int64) int {
	used := 0
	for _, h := range holders {
		if !h.Superseded && h.LeaseExpiresNS > nowNS {
			used += h.budgetCost()
		}
	}
	return used
}

func liveFloor(holders []s3Holder, nowNS int64) int {
	floor := 0
	for _, h := range holders {
		if h.Superseded || h.LeaseExpiresNS <= nowNS {
			continue
		}
		if h.Cost == 0 && h.DeclaredCapacity == inheritedS3HolderDeclaredCapacity {
			continue
		}
		term := h.DeclaredCapacity
		if term <= 0 {
			term = 1
		}
		if floor == 0 || term < floor {
			floor = term
		}
	}
	return floor
}

func effectiveCapacity(incoming, floor, entryCap int) int {
	eff := floor
	if incoming > 0 && (eff == 0 || incoming < eff) {
		eff = incoming
	}
	if eff > 0 {
		return eff
	}
	return entryCap
}

func fitsBudget(used, cost, capacity int) bool {
	return used <= capacity && cost <= capacity-used
}

func findHolder(doc *s3SlotDoc, holderID string) *s3Holder {
	for i := range doc.Holders {
		if doc.Holders[i].HolderID == holderID {
			return &doc.Holders[i]
		}
	}
	return nil
}

func upsertHolder(doc *s3SlotDoc, h s3Holder) {
	for i := range doc.Holders {
		if doc.Holders[i].HolderID == h.HolderID {
			doc.Holders[i] = h
			return
		}
	}
	doc.Holders = append(doc.Holders, h)
}

func supersedeS3HolderAndInherited(doc *s3SlotDoc, holderID string) []string {
	h := findHolder(doc, holderID)
	if h == nil || h.Superseded {
		return nil
	}
	inheritedMarker := inheritedS3HolderNodeID(holderID)
	originalMarker := inheritedMarker
	if isInheritedS3HolderNodeID(h.NodeID) {
		originalMarker = h.NodeID
	}
	h.Superseded = true
	ids := []string{holderID}
	for i := range doc.Holders {
		if doc.Holders[i].Superseded || (doc.Holders[i].NodeID != inheritedMarker && doc.Holders[i].NodeID != originalMarker) {
			continue
		}
		ids = append(ids, supersedeS3HolderAndInherited(doc, doc.Holders[i].HolderID)...)
	}
	return ids
}

func oldestLiveHolder(doc *s3SlotDoc, nowNS int64) *s3Holder {
	var best *s3Holder
	for i := range doc.Holders {
		h := &doc.Holders[i]
		if h.Superseded || h.LeaseExpiresNS <= nowNS {
			continue
		}
		if best == nil || h.ClaimedAtNS < best.ClaimedAtNS {
			best = h
		}
	}
	return best
}

// parkWaiter inserts or replaces the waiter for (runID, nodeID): a
// re-arrival overwrites its prior parked row, mirroring the store's
// primary-key replace.
func parkWaiter(doc *s3SlotDoc, w s3Waiter) {
	for i := range doc.Waiters {
		if doc.Waiters[i].RunID == w.RunID && doc.Waiters[i].NodeID == w.NodeID {
			doc.Waiters[i] = w
			return
		}
	}
	doc.Waiters = append(doc.Waiters, w)
}

func findWaiter(doc *s3SlotDoc, runID, nodeID string) *s3Waiter {
	for i := range doc.Waiters {
		if doc.Waiters[i].RunID == runID && doc.Waiters[i].NodeID == nodeID {
			return &doc.Waiters[i]
		}
	}
	return nil
}

func removeWaiter(doc *s3SlotDoc, runID, nodeID string) bool {
	for i := range doc.Waiters {
		if doc.Waiters[i].RunID == runID && doc.Waiters[i].NodeID == nodeID {
			doc.Waiters = append(doc.Waiters[:i], doc.Waiters[i+1:]...)
			return true
		}
	}
	return false
}

func removeHolderByRunNode(doc *s3SlotDoc, runID, nodeID string) bool {
	for i := range doc.Holders {
		if doc.Holders[i].RunID == runID && doc.Holders[i].NodeID == nodeID {
			doc.Holders = append(doc.Holders[:i], doc.Holders[i+1:]...)
			return true
		}
	}
	return false
}

func liveHolders(doc *s3SlotDoc, nowNS int64) []store.ConcurrencyHolder {
	var out []store.ConcurrencyHolder
	for _, h := range doc.Holders {
		if h.Superseded || h.LeaseExpiresNS <= nowNS {
			continue
		}
		out = append(out, toStoreHolder(h))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ClaimedAt.Before(out[j].ClaimedAt) })
	return out
}

func toStoreHolder(h s3Holder) store.ConcurrencyHolder {
	nodeID := h.NodeID
	declaredCapacity := h.DeclaredCapacity
	if declaredCapacity == inheritedS3HolderDeclaredCapacity {
		declaredCapacity = 0
	}
	if isInheritedS3HolderNodeID(nodeID) {
		nodeID = ""
	}
	return store.ConcurrencyHolder{
		HolderID:         h.HolderID,
		RunID:            h.RunID,
		NodeID:           nodeID,
		ClaimedAt:        time.Unix(0, h.ClaimedAtNS),
		LeaseExpiresAt:   time.Unix(0, h.LeaseExpiresNS),
		Superseded:       h.Superseded,
		Cost:             h.Cost,
		DeclaredCapacity: declaredCapacity,
	}
}

func recordFinished(doc *s3SlotDoc, runID, nodeID, outcome string, nowNS int64) {
	for i := range doc.Finished {
		if doc.Finished[i].RunID == runID && doc.Finished[i].NodeID == nodeID {
			doc.Finished[i].Outcome = outcome
			doc.Finished[i].RecordedNS = nowNS
			return
		}
	}
	doc.Finished = append(doc.Finished, s3FinishedHolder{RunID: runID, NodeID: nodeID, Outcome: outcome, RecordedNS: nowNS})
}

func finishedOutcome(doc *s3SlotDoc, runID, nodeID string) string {
	for _, f := range doc.Finished {
		if f.RunID == runID && f.NodeID == nodeID {
			return f.Outcome
		}
	}
	return ""
}

// prune drops expired holders (reclaiming their budget for the next
// acquirer), expired cache entries, and stale finished records. Applied
// inside any write path; never persisted on a read-only path.
func prune(doc *s3SlotDoc, nowNS int64) {
	if len(doc.Holders) > 0 {
		kept := doc.Holders[:0]
		for _, h := range doc.Holders {
			if h.LeaseExpiresNS > nowNS {
				kept = append(kept, h)
			}
		}
		doc.Holders = kept
	}
	for k, e := range doc.Cache {
		if e.ExpiresNS <= nowNS {
			delete(doc.Cache, k)
		}
	}
	if len(doc.Finished) > 0 {
		cutoff := nowNS - int64(s3FinishedRetention)
		kept := doc.Finished[:0]
		for _, f := range doc.Finished {
			if f.RecordedNS >= cutoff {
				kept = append(kept, f)
			}
		}
		doc.Finished = kept
	}
}

// promoteWaiters admits queue/cancel_others waiters in arrival order
// until the head of line no longer fits, mirroring the store's
// head-of-line-blocking promotion. Returns whether any were promoted.
func promoteWaiters(doc *s3SlotDoc, now time.Time, lease time.Duration) bool {
	nowNS := now.UnixNano()
	entryCap := doc.Capacity
	if entryCap <= 0 {
		entryCap = 1
	}
	used := liveHolderCost(doc.Holders, nowNS)
	holderMin := liveFloor(doc.Holders, nowNS)

	order := make([]int, 0, len(doc.Waiters))
	for i, w := range doc.Waiters {
		if w.Policy == store.OnLimitQueue || w.Policy == store.OnLimitCancelOthers {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		return doc.Waiters[order[a]].ArrivedAtNS < doc.Waiters[order[b]].ArrivedAtNS
	})

	remove := make(map[int]bool)
	for _, idx := range order {
		w := doc.Waiters[idx]
		cost := w.Cost
		if cost <= 0 {
			cost = 1
		}
		candCap := w.DeclaredCapacity
		if candCap <= 0 {
			candCap = entryCap
		}
		if holderMin > 0 && holderMin < candCap {
			candCap = holderMin
		}
		if !fitsBudget(used, cost, candCap) {
			break
		}
		hid := w.HolderID
		if hid == "" {
			hid = w.RunID + "/" + nodeOrDash(w.NodeID)
		}
		upsertHolder(doc, s3Holder{
			HolderID:         hid,
			RunID:            w.RunID,
			NodeID:           w.NodeID,
			ClaimedAtNS:      nowNS,
			LeaseExpiresNS:   now.Add(lease).UnixNano(),
			Cost:             cost,
			DeclaredCapacity: w.DeclaredCapacity,
		})
		remove[idx] = true
		used += cost
		if holderMin == 0 || candCap < holderMin {
			holderMin = candCap
		}
	}
	if len(remove) == 0 {
		return false
	}
	kept := doc.Waiters[:0]
	for i, w := range doc.Waiters {
		if !remove[i] {
			kept = append(kept, w)
		}
	}
	doc.Waiters = kept
	return true
}

func (c *s3Concurrency) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	if fb := c.fallback(ctx); fb != nil {
		return fb.AcquireSlot(ctx, req)
	}

	capacity := req.Capacity
	if capacity <= 0 {
		capacity = 1
	}
	policy := req.Policy
	if policy == "" {
		policy = store.OnLimitQueue
	}
	cost := req.Cost
	if cost <= 0 {
		cost = 1
	}
	lease := req.Lease
	if lease <= 0 {
		lease = store.DefaultConcurrencyLease
	}
	holderID := req.HolderID
	if holderID == "" {
		holderID = req.RunID + "/" + nodeOrDash(req.NodeID)
	}

	if req.InheritedHolderID != "" && req.CacheKeyHash != "" {
		return store.AcquireSlotResponse{}, errors.New("s3 concurrency: inherited holder cannot be used with cache acquisition")
	}
	if req.InheritedHolderID == "" && cost > capacity {
		if policy == store.OnLimitSkip {
			return store.AcquireSlotResponse{Kind: store.AcquireSkipped}, nil
		}
		return store.AcquireSlotResponse{Kind: store.AcquireFailed}, nil
	}

	var resp store.AcquireSlotResponse
	err := c.mutate(ctx, req.Key, func(doc *s3SlotDoc, _ bool, now time.Time) (bool, error) {
		nowNS := now.UnixNano()
		resp = store.AcquireSlotResponse{}
		doc.Key = req.Key
		prune(doc, nowNS)

		if req.CacheKeyHash != "" && !req.BypassRead {
			if hit, ok := doc.Cache[req.CacheKeyHash]; ok && hit.ExpiresNS > nowNS {
				resp = store.AcquireSlotResponse{
					Kind:         store.AcquireCached,
					OutputRef:    hit.OutputRef,
					OriginRunID:  hit.OriginRunID,
					OriginNodeID: hit.OriginNodeID,
				}
				return false, nil
			}
		}

		dirty := false
		if doc.Capacity <= 0 {
			doc.Capacity = capacity
			dirty = true
		} else if doc.Capacity != capacity {
			resp.PreviousCapacity = doc.Capacity
			resp.DriftNote = fmt.Sprintf("capacity for %q changed from %d to %d", req.Key, doc.Capacity, capacity)
			doc.Capacity = capacity
			dirty = true
		}
		entryCap := doc.Capacity

		if req.InheritedHolderID != "" {
			h := findHolder(doc, req.InheritedHolderID)
			if h == nil {
				return false, fmt.Errorf("s3 concurrency: inherited holder %q for key %q is not active", req.InheritedHolderID, req.Key)
			}
			if h.Superseded {
				return false, fmt.Errorf("%w: inherited holder %q for key %q", store.ErrConcurrencySuperseded, req.InheritedHolderID, req.Key)
			}
			if h.LeaseExpiresNS <= nowNS {
				return false, fmt.Errorf("s3 concurrency: inherited holder %q for key %q is not active", req.InheritedHolderID, req.Key)
			}
			h.LeaseExpiresNS = now.Add(lease).UnixNano()
			upsertHolder(doc, s3Holder{
				HolderID:         holderID,
				RunID:            req.RunID,
				NodeID:           inheritedS3HolderNodeID(req.InheritedHolderID),
				ClaimedAtNS:      nowNS,
				LeaseExpiresNS:   h.LeaseExpiresNS,
				Cost:             0,
				DeclaredCapacity: inheritedS3HolderDeclaredCapacity,
			})
			resp.Kind = store.AcquireGranted
			resp.HolderID = holderID
			resp.LeaseExpiresAt = time.Unix(0, h.LeaseExpiresNS)
			return true, nil
		}

		if h := findHolder(doc, holderID); h != nil && !h.Superseded && h.LeaseExpiresNS > nowNS {
			h.LeaseExpiresNS = now.Add(lease).UnixNano()
			resp.Kind = store.AcquireGranted
			resp.HolderID = holderID
			resp.LeaseExpiresAt = time.Unix(0, h.LeaseExpiresNS)
			return true, nil
		}

		used := liveHolderCost(doc.Holders, nowNS)
		floor := liveFloor(doc.Holders, nowNS)
		effCap := effectiveCapacity(capacity, floor, entryCap)

		effPolicy := policy
		if effPolicy == store.OnLimitCoalesce && req.BypassRead {
			effPolicy = store.OnLimitQueue
		}

		fifoBlocked := false
		if effPolicy == store.OnLimitQueue {
			for _, w := range doc.Waiters {
				if w.Policy == store.OnLimitQueue && (w.RunID != req.RunID || w.NodeID != req.NodeID) {
					fifoBlocked = true
					break
				}
			}
		}

		if !fifoBlocked && fitsBudget(used, cost, effCap) {
			expires := now.Add(lease).UnixNano()
			upsertHolder(doc, s3Holder{
				HolderID:         holderID,
				RunID:            req.RunID,
				NodeID:           req.NodeID,
				ClaimedAtNS:      nowNS,
				LeaseExpiresNS:   expires,
				Cost:             cost,
				DeclaredCapacity: capacity,
			})
			resp.Kind = store.AcquireGranted
			resp.HolderID = holderID
			resp.LeaseExpiresAt = time.Unix(0, expires)
			return true, nil
		}

		switch effPolicy {
		case store.OnLimitSkip:
			resp.Kind = store.AcquireSkipped
			return dirty, nil
		case store.OnLimitFail:
			resp.Kind = store.AcquireFailed
			return dirty, nil
		case store.OnLimitCoalesce:
			leader := oldestLiveHolder(doc, nowNS)
			var lr, ln string
			if leader != nil {
				lr, ln = leader.RunID, leader.NodeID
			}
			parkWaiter(doc, s3Waiter{
				RunID:            req.RunID,
				NodeID:           req.NodeID,
				HolderID:         holderID,
				ArrivedAtNS:      nowNS,
				Policy:           store.OnLimitCoalesce,
				CacheKeyHash:     req.CacheKeyHash,
				LeaderRunID:      lr,
				LeaderNodeID:     ln,
				Cost:             cost,
				DeclaredCapacity: capacity,
			})
			resp.Kind = store.AcquireCoalesced
			resp.LeaderRunID = lr
			resp.LeaderNodeID = ln
			return true, nil
		case store.OnLimitCancelOthers:
			var supersededIDs []string
			freed := 0
			for i := range doc.Holders {
				h := &doc.Holders[i]
				if h.Superseded || h.LeaseExpiresNS <= nowNS {
					continue
				}
				if fitsBudget(used-freed, cost, effCap) && len(supersededIDs) > 0 {
					break
				}
				supersededIDs = append(supersededIDs, supersedeS3HolderAndInherited(doc, h.HolderID)...)
				freed += h.budgetCost()
			}
			expires := now.Add(lease).UnixNano()
			upsertHolder(doc, s3Holder{
				HolderID:         holderID,
				RunID:            req.RunID,
				NodeID:           req.NodeID,
				ClaimedAtNS:      nowNS,
				LeaseExpiresNS:   expires,
				Cost:             cost,
				DeclaredCapacity: capacity,
			})
			resp.Kind = store.AcquireCancellingOthers
			resp.SupersededIDs = supersededIDs
			resp.HolderID = holderID
			resp.LeaseExpiresAt = time.Unix(0, expires)
			return true, nil
		default:
			parkWaiter(doc, s3Waiter{
				RunID:            req.RunID,
				NodeID:           req.NodeID,
				HolderID:         holderID,
				ArrivedAtNS:      nowNS,
				Policy:           store.OnLimitQueue,
				CacheKeyHash:     req.CacheKeyHash,
				Cost:             cost,
				DeclaredCapacity: capacity,
			})
			parked := findWaiter(doc, req.RunID, req.NodeID)
			position, queueLen := 0, 0
			for _, w := range doc.Waiters {
				if w.Policy != store.OnLimitQueue {
					continue
				}
				queueLen++
				if parked != nil && w.ArrivedAtNS < parked.ArrivedAtNS {
					position++
				}
			}
			resp.Kind = store.AcquireQueued
			resp.Position = position
			resp.QueueLength = queueLen
			resp.Holders = liveHolders(doc, nowNS)
			return true, nil
		}
	})
	return resp, err
}

func (c *s3Concurrency) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	if fb := c.fallback(ctx); fb != nil {
		return fb.HeartbeatSlot(ctx, key, holderID, lease)
	}
	if lease <= 0 {
		lease = store.DefaultConcurrencyLease
	}
	var expires time.Time
	var superseded bool
	err := c.mutate(ctx, key, func(doc *s3SlotDoc, exists bool, now time.Time) (bool, error) {
		if !exists {
			return false, store.ErrLockHeld
		}
		nowNS := now.UnixNano()
		h := findHolder(doc, holderID)
		if h == nil || h.LeaseExpiresNS <= nowNS {
			return false, store.ErrLockHeld
		}
		h.LeaseExpiresNS = now.Add(lease).UnixNano()
		expires = time.Unix(0, h.LeaseExpiresNS)
		superseded = h.Superseded
		return true, nil
	})
	if err != nil {
		return time.Time{}, false, err
	}
	return expires, superseded, nil
}

func (c *s3Concurrency) ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error) {
	if fb := c.fallback(ctx); fb != nil {
		return fb.ObserveSlot(ctx, key, holderID)
	}
	doc, _, exists, err := c.load(ctx, s3SlotKey(key))
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, store.ErrNotFound
	}
	h := findHolder(doc, holderID)
	if h == nil || h.LeaseExpiresNS <= time.Now().UnixNano() {
		return nil, store.ErrNotFound
	}
	holder := toStoreHolder(*h)
	holder.Key = key
	return &holder, nil
}

func (c *s3Concurrency) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	if fb := c.fallback(ctx); fb != nil {
		return fb.ReleaseSlot(ctx, key, holderID, outcome, outputRef, cacheKeyHash, ttl)
	}
	return c.mutate(ctx, key, func(doc *s3SlotDoc, exists bool, now time.Time) (bool, error) {
		if !exists {
			return false, nil
		}
		nowNS := now.UnixNano()

		var rRun, rNode string
		released := false
		for i := range doc.Holders {
			if doc.Holders[i].HolderID == holderID {
				releasedHolder := doc.Holders[i]
				rRun, rNode = doc.Holders[i].RunID, doc.Holders[i].NodeID
				if releasedHolder.Cost > 0 && !releasedHolder.Superseded && releasedHolder.LeaseExpiresNS > nowNS {
					inheritedMarker := inheritedS3HolderNodeID(releasedHolder.HolderID)
					originalMarker := inheritedMarker
					if isInheritedS3HolderNodeID(releasedHolder.NodeID) {
						originalMarker = releasedHolder.NodeID
					}
					for j := range doc.Holders {
						if i == j {
							continue
						}
						if doc.Holders[j].Superseded || doc.Holders[j].LeaseExpiresNS <= nowNS {
							continue
						}
						if doc.Holders[j].Cost == 0 &&
							doc.Holders[j].DeclaredCapacity == inheritedS3HolderDeclaredCapacity &&
							(doc.Holders[j].NodeID == inheritedMarker || doc.Holders[j].NodeID == originalMarker) {
							doc.Holders[j].Cost = releasedHolder.Cost
							doc.Holders[j].DeclaredCapacity = releasedHolder.DeclaredCapacity
							break
						}
					}
				}
				doc.Holders = append(doc.Holders[:i], doc.Holders[i+1:]...)
				released = true
				break
			}
		}
		if !released {
			rRun, rNode = splitHolderID(holderID)
		}

		changed := released
		if outcome == "success" && cacheKeyHash != "" && ttl > 0 {
			doc.Cache[cacheKeyHash] = s3Cache{
				OutputRef:    outputRef,
				OriginRunID:  rRun,
				OriginNodeID: rNode,
				CreatedNS:    nowNS,
				ExpiresNS:    now.Add(ttl).UnixNano(),
			}
			changed = true
		}

		drained := false
		kept := doc.Waiters[:0]
		for _, w := range doc.Waiters {
			if w.Policy == store.OnLimitCoalesce && w.LeaderRunID == rRun && w.LeaderNodeID == rNode {
				drained = true
				continue
			}
			kept = append(kept, w)
		}
		doc.Waiters = kept
		if drained {
			recordFinished(doc, rRun, rNode, outcome, nowNS)
			changed = true
		}

		prune(doc, nowNS)
		if promoteWaiters(doc, now, store.DefaultConcurrencyLease) {
			changed = true
		}
		return changed, nil
	})
}

func (c *s3Concurrency) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error) {
	if fb := c.fallback(ctx); fb != nil {
		return fb.ResolveWaiter(ctx, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID, bypassRead)
	}
	var resolution store.WaiterResolution
	err := c.mutate(ctx, key, func(doc *s3SlotDoc, exists bool, now time.Time) (bool, error) {
		if !exists {
			if leaderRunID != "" {
				resolution = store.WaiterResolution{Status: store.WaiterLeaderFinished, LeaderRunID: leaderRunID, LeaderNodeID: leaderNodeID}
			} else {
				resolution = store.WaiterResolution{Status: store.WaiterCancelled}
			}
			return false, nil
		}
		nowNS := now.UnixNano()
		beforeHolders := len(doc.Holders)
		beforeCache := len(doc.Cache)
		beforeFinished := len(doc.Finished)
		prune(doc, nowNS)
		changed := len(doc.Holders) != beforeHolders || len(doc.Cache) != beforeCache || len(doc.Finished) != beforeFinished
		if promoteWaiters(doc, now, store.DefaultConcurrencyLease) {
			changed = true
		}

		for _, h := range doc.Holders {
			if h.RunID == runID && h.NodeID == nodeID && !h.Superseded {
				resolution = store.WaiterResolution{
					Status:             store.WaiterPromoted,
					HolderID:           h.HolderID,
					HolderLeaseExpires: time.Unix(0, h.LeaseExpiresNS),
				}
				return changed, nil
			}
		}

		if cacheKeyHash != "" && !bypassRead {
			if hit, ok := doc.Cache[cacheKeyHash]; ok && hit.ExpiresNS > nowNS {
				resolution = store.WaiterResolution{
					Status:       store.WaiterCached,
					OutputRef:    hit.OutputRef,
					OriginRunID:  hit.OriginRunID,
					OriginNodeID: hit.OriginNodeID,
				}
				return changed, nil
			}
		}

		if w := findWaiter(doc, runID, nodeID); w != nil {
			position := 0
			queueLength := 0
			for _, x := range doc.Waiters {
				if x.Policy == store.OnLimitQueue || x.Policy == store.OnLimitCancelOthers {
					queueLength++
					if x.ArrivedAtNS < w.ArrivedAtNS {
						position++
					}
				}
			}
			resolution = store.WaiterResolution{Status: store.WaiterStillWaiting, Position: position, QueueLength: queueLength, Holders: liveHolders(doc, nowNS)}
			return changed, nil
		}

		if leaderRunID != "" {
			resolution = store.WaiterResolution{
				Status:        store.WaiterLeaderFinished,
				LeaderRunID:   leaderRunID,
				LeaderNodeID:  leaderNodeID,
				LeaderOutcome: finishedOutcome(doc, leaderRunID, leaderNodeID),
			}
		} else {
			resolution = store.WaiterResolution{Status: store.WaiterCancelled}
		}
		return changed, nil
	})
	if err != nil {
		return store.WaiterResolution{}, err
	}
	return resolution, nil
}

func (c *s3Concurrency) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	if fb := c.fallback(ctx); fb != nil {
		return fb.ForceReleaseSuperseded(ctx, key)
	}
	var dropped []store.ConcurrencyHolder
	err := c.mutate(ctx, key, func(doc *s3SlotDoc, exists bool, now time.Time) (bool, error) {
		dropped = nil
		if !exists {
			return false, nil
		}
		nowNS := now.UnixNano()
		kept := doc.Holders[:0]
		for _, h := range doc.Holders {
			if h.Superseded {
				dropped = append(dropped, toStoreHolder(h))
				continue
			}
			kept = append(kept, h)
		}
		if len(dropped) == 0 {
			return false, nil
		}
		doc.Holders = kept
		prune(doc, nowNS)
		promoteWaiters(doc, now, store.DefaultConcurrencyLease)
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return dropped, nil
}

func (c *s3Concurrency) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	if fb := c.fallback(ctx); fb != nil {
		return fb.CancelWaiter(ctx, key, runID, nodeID)
	}
	var removed bool
	err := c.mutate(ctx, key, func(doc *s3SlotDoc, exists bool, now time.Time) (bool, error) {
		removed = false
		if !exists {
			return false, nil
		}
		nowNS := now.UnixNano()
		waiterDeleted := removeWaiter(doc, runID, nodeID)
		holderDeleted := removeHolderByRunNode(doc, runID, nodeID)
		removed = waiterDeleted || holderDeleted
		if holderDeleted {
			prune(doc, nowNS)
			promoteWaiters(doc, now, store.DefaultConcurrencyLease)
		}
		return removed, nil
	})
	return removed, err
}

// splitHolderID recovers (runID, nodeID) from the "runID/nodeID"
// convention, splitting on the last separator so a runID with no
// separator is preserved. A trailing "-" decodes to an empty nodeID.
func splitHolderID(holderID string) (string, string) {
	for i := len(holderID) - 1; i >= 0; i-- {
		if holderID[i] == '/' {
			node := holderID[i+1:]
			if node == "-" {
				node = ""
			}
			return holderID[:i], node
		}
	}
	return holderID, ""
}
