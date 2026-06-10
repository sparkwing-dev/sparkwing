package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// fitsBudget reports whether an arrival of the given cost fits a key's
// effective capacity on top of the already-used cost. It compares by
// subtraction (cost <= capacity-used) rather than summing used+cost, so
// a very large declared cost can't overflow the sum into a false "fits"
// and over-admit.
func fitsBudget(used, cost, capacity int) bool {
	return used <= capacity && cost <= capacity-used
}

// OnLimit policies the coordination layer understands. Queue, Skip,
// Fail, and CancelOthers map to the SDK's sparkwing.OnLimit values for
// concurrency groups. Coalesce is the leader/follower policy that backs
// content-keyed memoization: the orchestrator acquires a capacity-1
// Coalesce slot keyed on a node's content hash, so identical work
// dedupes in flight and shares one cache entry. No concurrency group
// emits Coalesce.
const (
	OnLimitQueue        = "queue"
	OnLimitCoalesce     = "coalesce"
	OnLimitSkip         = "skip"
	OnLimitFail         = "fail"
	OnLimitCancelOthers = "cancel_others"
)

// DefaultConcurrencyLease matches DefaultLeaseDuration; a holder must
// be silent for the full window to be reaped.
const DefaultConcurrencyLease = 3 * time.Minute

// DefaultConcurrencyHeartbeatInterval shares cadence with trigger and
// run heartbeats so CancelOthers supersedes land within ~3s.
const DefaultConcurrencyHeartbeatInterval = 3 * time.Second

// DefaultConcurrencyHeartbeatTimeout is strictly less than the interval
// so a wedged controller can't stack ticks.
const DefaultConcurrencyHeartbeatTimeout = 2 * time.Second

// DefaultCancelTimeout bounds CancelOthers eviction.
const DefaultCancelTimeout = 60 * time.Second

// AcquireKind covers user-selectable + non-selectable outcomes.
type AcquireKind string

const (
	AcquireGranted          AcquireKind = "granted"
	AcquireQueued           AcquireKind = "queued"
	AcquireCoalesced        AcquireKind = "coalesced"
	AcquireSkipped          AcquireKind = "skipped"
	AcquireFailed           AcquireKind = "failed"
	AcquireCached           AcquireKind = "cached"
	AcquireCancellingOthers AcquireKind = "cancelling_others"
)

// AcquireSlotRequest: empty CacheKeyHash = no memo; empty NodeID =
// plan-level. HolderID convention: "runID/nodeID" or "runID/-".
//
// BypassRead suppresses the cache-lookup branch: the request flows
// straight into the capacity/coalesce/queue path as if no prior entry
// existed for this key. Cache WRITES at release time are unaffected.
// Used by --no-cache so a run forces fresh execution but still
// populates the runs store for subsequent runs over the same content.
type AcquireSlotRequest struct {
	Key           string
	HolderID      string
	RunID         string
	NodeID        string
	Capacity      int
	Policy        string
	CacheKeyHash  string
	CacheTTL      time.Duration
	CancelTimeout time.Duration
	Lease         time.Duration
	BypassRead    bool

	// Cost is the admission weight this arrival draws from the key's
	// capacity (author-defined units). Zero or negative is treated as
	// 1. Admission compares the summed cost of live holders plus this
	// cost against the effective capacity, not a slot count.
	Cost int
}

// AcquireSlotResponse: fields are populated per Kind.
type AcquireSlotResponse struct {
	Kind             AcquireKind
	HolderID         string
	LeaseExpiresAt   time.Time
	LeaderRunID      string
	LeaderNodeID     string
	OutputRef        string
	OriginRunID      string
	OriginNodeID     string
	SupersededIDs    []string
	PreviousCapacity int
	DriftNote        string

	// Observability for a queued arrival (Kind == AcquireQueued):
	// Position is the number of queue-policy waiters that arrived
	// earlier (0 == next in line), QueueLength is the total queued for
	// the key, and Holders are the slots currently held. Zero/empty for
	// other kinds.
	Position    int
	QueueLength int
	Holders     []ConcurrencyHolder
}

// ConcurrencyHolder mirrors the concurrency_holders row.
type ConcurrencyHolder struct {
	Key            string
	HolderID       string
	RunID          string
	NodeID         string
	ClaimedAt      time.Time
	LeaseExpiresAt time.Time
	Superseded     bool

	// Cost is the admission weight this holder draws from the key's
	// budget; DeclaredCapacity is the capacity it declared. Populated by
	// GetConcurrencyState for the operator budget view; zero on the hot
	// acquire/promote paths that don't need them.
	Cost             int
	DeclaredCapacity int
}

// ConcurrencyWaiter mirrors the concurrency_waiters row.
type ConcurrencyWaiter struct {
	Key           string
	RunID         string
	NodeID        string
	HolderID      string
	ArrivedAt     time.Time
	Policy        string
	CacheKeyHash  string
	LeaderRunID   string
	LeaderNodeID  string
	CancelTimeout time.Duration

	// Cost is the admission weight this waiter will draw once promoted;
	// DeclaredCapacity is the capacity this waiter declared, a
	// participant in the most-restrictive-wins minimum.
	Cost             int
	DeclaredCapacity int

	// Position is the waiter's 0-based rank among queue-policy waiters
	// for the key, in arrival order, as derived by GetConcurrencyState.
	// Zero for non-queue waiters.
	Position int
}

// ConcurrencyState: Capacity is the last-declared capacity;
// EffectiveCapacity is the most-restrictive minimum actually enforced
// over live participants; UsedCost is the summed cost of active
// (non-superseded, unexpired) holders. Available budget is
// EffectiveCapacity - UsedCost. Rows are oldest-first.
type ConcurrencyState struct {
	Key               string
	Capacity          int
	EffectiveCapacity int
	UsedCost          int
	Holders           []ConcurrencyHolder
	Waiters           []ConcurrencyWaiter
}

// AcquireConcurrencySlot atomically performs cache-lookup, capacity
// upsert, holder-count, and the policy branch in one txn. Cancel
// dispatch is the controller's job; SupersededIDs lists the targets.
func (s *Store) AcquireConcurrencySlot(ctx context.Context, req AcquireSlotRequest) (AcquireSlotResponse, error) {
	if req.Key == "" {
		return AcquireSlotResponse{}, errors.New("concurrency: empty key")
	}
	if req.HolderID == "" {
		return AcquireSlotResponse{}, errors.New("concurrency: empty holder_id")
	}
	if req.Policy == "" {
		req.Policy = OnLimitQueue
	}
	if req.Capacity <= 0 {
		req.Capacity = 1
	}
	if req.Cost <= 0 {
		req.Cost = 1
	}
	if req.Lease <= 0 {
		req.Lease = DefaultConcurrencyLease
	}
	if req.CancelTimeout <= 0 {
		req.CancelTimeout = DefaultCancelTimeout
	}

	// An arrival whose cost exceeds the key's capacity can never be
	// admitted -- the open budget never reaches its cost even when the
	// key is idle. Queuing it would strand it forever (default
	// QueueTimeout is "wait indefinitely"), so reject up front: Skip
	// resolves it as a no-op, every other policy fails it. The SDK has a
	// plan-time guard for the common case; this is the backstop for
	// version skew (another writer lowered capacity below this cost).
	if req.Cost > req.Capacity {
		if req.Policy == OnLimitSkip {
			return AcquireSlotResponse{Kind: AcquireSkipped}, nil
		}
		return AcquireSlotResponse{Kind: AcquireFailed}, nil
	}

	now := time.Now()
	nowNS := now.UnixNano()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Cache lookup; atomic with the rest so we never double-run.
	// BypassRead skips this branch entirely: the request flows into
	// capacity / coalesce / queue as if no prior entry existed. The
	// release-time write still records the run's result so a
	// follow-up request (BypassRead=false) hits cache normally.
	if req.CacheKeyHash != "" && !req.BypassRead {
		var outputRef, originRun, originNode string
		var expiresNS int64
		err := tx.QueryRowContext(
			ctx,
			`SELECT output_ref, origin_run_id, origin_node_id, expires_at
			   FROM concurrency_cache
			  WHERE key = ? AND cache_key_hash = ?`,
			req.Key, req.CacheKeyHash,
		).Scan(&outputRef, &originRun, &originNode, &expiresNS)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// miss; fall through
		case err != nil:
			return AcquireSlotResponse{}, err
		default:
			if expiresNS > nowNS {
				if _, err := tx.ExecContext(
					ctx,
					`UPDATE concurrency_cache SET last_hit_at = ? WHERE key = ? AND cache_key_hash = ?`,
					nowNS, req.Key, req.CacheKeyHash,
				); err != nil {
					return AcquireSlotResponse{}, err
				}
				if err := tx.Commit(); err != nil {
					return AcquireSlotResponse{}, err
				}
				return AcquireSlotResponse{
					Kind:         AcquireCached,
					OutputRef:    outputRef,
					OriginRunID:  originRun,
					OriginNodeID: originNode,
				}, nil
			}
			// expired entry; delete so we don't keep re-reading it
			if _, err := tx.ExecContext(
				ctx,
				`DELETE FROM concurrency_cache WHERE key = ? AND cache_key_hash = ? AND expires_at <= ?`,
				req.Key, req.CacheKeyHash, nowNS,
			); err != nil {
				return AcquireSlotResponse{}, err
			}
		}
	}

	// 2. Upsert entry (latest-wins on capacity; drift note on change).
	// On Postgres the SELECT also acquires a row-level lock so
	// concurrent AcquireConcurrencySlot calls for the same key
	// serialize through the rest of the transaction (capacity check,
	// holder count, policy branch). The ON CONFLICT DO NOTHING on the
	// first-write path closes the race where two transactions discover
	// the row missing simultaneously.
	driftNote := ""
	prevCap := 0
	var existingCap int
	err = tx.QueryRowContext(
		ctx,
		`SELECT capacity FROM concurrency_entries WHERE key = ?`+s.forUpdate(), req.Key,
	).Scan(&existingCap)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO concurrency_entries
			   (key, capacity, previous_capacity, last_write_run_id, last_write_node_id, updated_at)
			 VALUES (?, ?, NULL, ?, ?, ?)
			 ON CONFLICT (key) DO NOTHING`,
			req.Key, req.Capacity, req.RunID, req.NodeID, nowNS,
		); err != nil {
			return AcquireSlotResponse{}, err
		}
	case err != nil:
		return AcquireSlotResponse{}, err
	default:
		if existingCap != req.Capacity {
			prevCap = existingCap
			driftNote = fmt.Sprintf(
				"concurrency key %q capacity changed %d -> %d (latest-wins; previous writer was preserved in previous_capacity)",
				req.Key, existingCap, req.Capacity,
			)
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE concurrency_entries
				    SET capacity = ?, previous_capacity = ?, last_write_run_id = ?, last_write_node_id = ?, updated_at = ?
				  WHERE key = ?`,
				req.Capacity, existingCap, req.RunID, req.NodeID, nowNS, req.Key,
			); err != nil {
				return AcquireSlotResponse{}, err
			}
		} else {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE concurrency_entries
				    SET last_write_run_id = ?, last_write_node_id = ?, updated_at = ?
				  WHERE key = ?`,
				req.RunID, req.NodeID, nowNS, req.Key,
			); err != nil {
				return AcquireSlotResponse{}, err
			}
		}
	}

	// 3a. Idempotent re-acquire by same holder_id; refreshes lease. An
	// expired row must NOT short-circuit here: its budget may already be
	// reassigned, so reviving it would over-admit. Let it fall through to
	// the capacity check, where the ON CONFLICT insert reclaims it.
	var existingLeaseNS int64
	var existingSuperInt int
	err = tx.QueryRowContext(
		ctx,
		`SELECT lease_expires_at, superseded FROM concurrency_holders
		  WHERE key = ? AND holder_id = ?`,
		req.Key, req.HolderID,
	).Scan(&existingLeaseNS, &existingSuperInt)
	if err == nil && existingSuperInt == 0 && existingLeaseNS > nowNS {
		newExpires := now.Add(req.Lease).UnixNano()
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE concurrency_holders SET lease_expires_at = ? WHERE key = ? AND holder_id = ?`,
			newExpires, req.Key, req.HolderID,
		); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := tx.Commit(); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{
			Kind:             AcquireGranted,
			HolderID:         req.HolderID,
			LeaseExpiresAt:   time.Unix(0, newExpires),
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
		}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AcquireSlotResponse{}, err
	}

	// 3. Sum active (non-superseded, unexpired) holder cost and resolve
	// the effective capacity (most-restrictive over live participants
	// plus this arrival's declaration).
	activeCost, err := txSumActiveCost(ctx, tx, req.Key, nowNS)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	effCap, err := txEffectiveCapacity(ctx, tx, req.Key, nowNS, req.Capacity)
	if err != nil {
		return AcquireSlotResponse{}, err
	}

	// 4. Budget available -> grant immediately.
	if fitsBudget(activeCost, req.Cost, effCap) {
		expiresNS := now.Add(req.Lease).UnixNano()
		// ON CONFLICT takes the slot cleanly when a row with this
		// holder_id already exists but is superseded (a CancelOthers
		// eviction not yet reaped). A same-holder_id re-acquire --
		// deterministic runID/nodeID, reachable on crash/redeliver --
		// would otherwise hit a UNIQUE violation. The non-superseded
		// live holder is handled earlier by the idempotent re-acquire
		// branch, so reaching here means the existing row is superseded
		// or expired; reclaim it (fresh lease, cleared supersede).
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO concurrency_holders
			   (key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded, cost, declared_capacity)
			 VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)
			 ON CONFLICT (key, holder_id) DO UPDATE SET
			   run_id            = excluded.run_id,
			   node_id           = excluded.node_id,
			   claimed_at        = excluded.claimed_at,
			   lease_expires_at  = excluded.lease_expires_at,
			   superseded        = 0,
			   cost              = excluded.cost,
			   declared_capacity = excluded.declared_capacity`,
			req.Key, req.HolderID, req.RunID, req.NodeID, nowNS, expiresNS, req.Cost, req.Capacity,
		); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := tx.Commit(); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{
			Kind:             AcquireGranted,
			HolderID:         req.HolderID,
			LeaseExpiresAt:   time.Unix(0, expiresNS),
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
		}, nil
	}

	// 5. Slot full -> branch on the arrival's policy.
	switch req.Policy {
	case OnLimitSkip:
		if err := tx.Commit(); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{Kind: AcquireSkipped, PreviousCapacity: prevCap, DriftNote: driftNote}, nil

	case OnLimitFail:
		if err := tx.Commit(); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{Kind: AcquireFailed, PreviousCapacity: prevCap, DriftNote: driftNote}, nil

	case OnLimitCoalesce:
		var leaderRun, leaderNode string
		err := tx.QueryRowContext(
			ctx,
			`SELECT run_id, node_id FROM concurrency_holders
			  WHERE key = ? AND superseded = 0
			  ORDER BY claimed_at ASC LIMIT 1`,
			req.Key,
		).Scan(&leaderRun, &leaderNode)
		if err != nil {
			return AcquireSlotResponse{}, fmt.Errorf("coalesce: select leader: %w", err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO concurrency_waiters
			   (key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
			 ON CONFLICT (key, run_id, node_id) DO UPDATE SET
			   holder_id         = excluded.holder_id,
			   arrived_at        = excluded.arrived_at,
			   policy            = excluded.policy,
			   cache_key_hash    = excluded.cache_key_hash,
			   leader_run_id     = excluded.leader_run_id,
			   leader_node_id    = excluded.leader_node_id,
			   cancel_timeout_ns = excluded.cancel_timeout_ns,
			   cost              = excluded.cost,
			   declared_capacity = excluded.declared_capacity`,
			req.Key, req.RunID, req.NodeID, req.HolderID, nowNS, OnLimitCoalesce, req.CacheKeyHash, leaderRun, leaderNode, req.Cost, req.Capacity,
		); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := tx.Commit(); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{
			Kind:             AcquireCoalesced,
			LeaderRunID:      leaderRun,
			LeaderNodeID:     leaderNode,
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
		}, nil

	case OnLimitCancelOthers:
		// Evict oldest holders first until freeing their cost would let
		// this arrival's cost fit the effective capacity. Always evict
		// at least one so a single over-capacity arrival still proceeds.
		rows, err := tx.QueryContext(
			ctx,
			`SELECT holder_id, cost FROM concurrency_holders
			  WHERE key = ? AND superseded = 0 AND lease_expires_at > ?
			  ORDER BY claimed_at ASC`,
			req.Key, nowNS,
		)
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		type heldCost struct {
			id   string
			cost int
		}
		var held []heldCost
		for rows.Next() {
			var hc heldCost
			if err := rows.Scan(&hc.id, &hc.cost); err != nil {
				_ = rows.Close()
				return AcquireSlotResponse{}, err
			}
			held = append(held, hc)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return AcquireSlotResponse{}, err
		}
		var supersededIDs []string
		freed := 0
		for _, hc := range held {
			if fitsBudget(activeCost-freed, req.Cost, effCap) && len(supersededIDs) > 0 {
				break
			}
			supersededIDs = append(supersededIDs, hc.id)
			freed += hc.cost
		}
		for _, hid := range supersededIDs {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE concurrency_holders SET superseded = 1 WHERE key = ? AND holder_id = ?`,
				req.Key, hid,
			); err != nil {
				return AcquireSlotResponse{}, err
			}
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO concurrency_waiters
			   (key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity)
			 VALUES (?, ?, ?, ?, ?, ?, ?, '', '', ?, ?, ?)
			 ON CONFLICT (key, run_id, node_id) DO UPDATE SET
			   holder_id         = excluded.holder_id,
			   arrived_at        = excluded.arrived_at,
			   policy            = excluded.policy,
			   cache_key_hash    = excluded.cache_key_hash,
			   leader_run_id     = excluded.leader_run_id,
			   leader_node_id    = excluded.leader_node_id,
			   cancel_timeout_ns = excluded.cancel_timeout_ns,
			   cost              = excluded.cost,
			   declared_capacity = excluded.declared_capacity`,
			req.Key, req.RunID, req.NodeID, req.HolderID, nowNS, OnLimitCancelOthers, req.CacheKeyHash, int64(req.CancelTimeout), req.Cost, req.Capacity,
		); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := tx.Commit(); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{
			Kind:             AcquireCancellingOthers,
			SupersededIDs:    supersededIDs,
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
		}, nil

	case OnLimitQueue:
		fallthrough
	default:
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO concurrency_waiters
			   (key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity)
			 VALUES (?, ?, ?, ?, ?, ?, ?, '', '', 0, ?, ?)
			 ON CONFLICT (key, run_id, node_id) DO UPDATE SET
			   holder_id         = excluded.holder_id,
			   arrived_at        = excluded.arrived_at,
			   policy            = excluded.policy,
			   cache_key_hash    = excluded.cache_key_hash,
			   leader_run_id     = excluded.leader_run_id,
			   leader_node_id    = excluded.leader_node_id,
			   cancel_timeout_ns = excluded.cancel_timeout_ns,
			   cost              = excluded.cost,
			   declared_capacity = excluded.declared_capacity`,
			req.Key, req.RunID, req.NodeID, req.HolderID, nowNS, OnLimitQueue, req.CacheKeyHash, req.Cost, req.Capacity,
		); err != nil {
			return AcquireSlotResponse{}, err
		}
		// Observability: this arrival's rank among queue waiters, the
		// total queued, and who holds the slots -- computed in the same
		// transaction so they're consistent with the queue this wait
		// joined. arrived_at < nowNS excludes the just-inserted self.
		var position, queueLen int
		if err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE key = ? AND policy = ? AND arrived_at < ?`,
			req.Key, OnLimitQueue, nowNS,
		).Scan(&position); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE key = ? AND policy = ?`,
			req.Key, OnLimitQueue,
		).Scan(&queueLen); err != nil {
			return AcquireSlotResponse{}, err
		}
		holders, err := txActiveHolders(ctx, tx, req.Key, nowNS)
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := tx.Commit(); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{
			Kind:             AcquireQueued,
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
			Position:         position,
			QueueLength:      queueLen,
			Holders:          holders,
		}, nil
	}
}

// txActiveHolders reads the current non-superseded, unexpired holders
// for a key within an open transaction, oldest claim first.
func txActiveHolders(ctx context.Context, tx *storeTx, key string, nowNS int64) ([]ConcurrencyHolder, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded
		   FROM concurrency_holders
		  WHERE key = ? AND superseded = 0 AND lease_expires_at > ?
		  ORDER BY claimed_at ASC`,
		key, nowNS,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConcurrencyHolder
	for rows.Next() {
		var h ConcurrencyHolder
		var claimedNS, expiresNS int64
		var superInt int
		if err := rows.Scan(&h.Key, &h.HolderID, &h.RunID, &h.NodeID, &claimedNS, &expiresNS, &superInt); err != nil {
			return nil, err
		}
		h.ClaimedAt = time.Unix(0, claimedNS)
		h.LeaseExpiresAt = time.Unix(0, expiresNS)
		h.Superseded = superInt == 1
		out = append(out, h)
	}
	return out, rows.Err()
}

// txSumActiveCost returns the summed admission cost of the key's
// active (non-superseded, unexpired) holders.
func txSumActiveCost(ctx context.Context, tx *storeTx, key string, nowNS int64) (int, error) {
	var sum sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(SUM(cost), 0) FROM concurrency_holders
		  WHERE key = ? AND superseded = 0 AND lease_expires_at > ?`,
		key, nowNS,
	).Scan(&sum); err != nil {
		return 0, err
	}
	return int(sum.Int64), nil
}

// txEffectiveCapacity resolves the most-restrictive-wins capacity for a
// key: the minimum declared_capacity over the **admitted holders** plus
// the incoming arrival's declared capacity. Parked waiters are NOT
// folded in -- a non-admitted waiter holds no budget, so letting its
// declaration drag the effective capacity below the already-admitted
// holders would invert priority and starve a FIFO head that fits under
// its own capacity. A non-positive incoming is ignored (the release
// path has no arrival; the promote path folds each candidate's own
// capacity itself). When no admitted holder has a positive
// declared_capacity, it falls back to the entries row, then to 1.
func txEffectiveCapacity(ctx context.Context, tx *storeTx, key string, nowNS int64, incoming int) (int, error) {
	entryCap, err := txEntryCapacity(ctx, tx, key)
	if err != nil {
		return 0, err
	}
	// A live holder with declared_capacity<=0 (a v3-migration backfill or a
	// promoted legacy waiter) still counts toward used cost, so it must
	// count toward the floor too. Its real declaration is unknown, so it
	// contributes the most-restrictive capacity (1) rather than vanishing
	// from the floor and inflating effective capacity into over-admission.
	var minDeclared sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT MIN(CASE WHEN declared_capacity > 0 THEN declared_capacity ELSE 1 END)
		   FROM concurrency_holders
		  WHERE key = ? AND superseded = 0 AND lease_expires_at > ?`,
		key, nowNS,
	).Scan(&minDeclared); err != nil {
		return 0, err
	}

	eff := 0
	if minDeclared.Valid && minDeclared.Int64 > 0 {
		eff = int(minDeclared.Int64)
	}
	if incoming > 0 && (eff == 0 || incoming < eff) {
		eff = incoming
	}
	if eff > 0 {
		return eff, nil
	}
	return entryCap, nil
}

// txEntryCapacity returns the registered capacity for a key, defaulting
// to 1 when the entry row is missing or non-positive.
func txEntryCapacity(ctx context.Context, tx *storeTx, key string) (int, error) {
	var entryCap int
	err := tx.QueryRowContext(
		ctx, `SELECT capacity FROM concurrency_entries WHERE key = ?`, key,
	).Scan(&entryCap)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	if entryCap <= 0 {
		return 1, nil
	}
	return entryCap, nil
}

// txPromoteWaiters grants holder rows to FIFO queue / cancel_others
// waiters, summing each waiter's declared cost against the open budget
// (effective capacity minus live holder cost). A heavy waiter at the
// head of the queue is not skipped by a cheaper one behind it: when the
// head no longer fits, promotion stops. Coalesce waiters resolve via
// the leader path, not here. Returns the promoted waiters with their
// assigned HolderID set.
func txPromoteWaiters(ctx context.Context, tx *storeTx, key string, nowNS, expiresNS int64) ([]ConcurrencyWaiter, error) {
	// holderMin is the most-restrictive declared capacity over the
	// currently-admitted holders (0 == no admitted holder constrains
	// the budget). entryCap is the declared default used for legacy
	// waiter rows that carry no declared_capacity. Parked waiters are
	// deliberately NOT folded in here -- each candidate's own declared
	// capacity is the ceiling that gates its own promotion.
	entryCap, err := txEntryCapacity(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	// Zero-cap live holders (migration backfill / promoted legacy waiters)
	// contribute the most-restrictive capacity (1) here too, so they
	// constrain promotion instead of vanishing from the floor.
	var holderMinNull sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT MIN(CASE WHEN declared_capacity > 0 THEN declared_capacity ELSE 1 END)
		   FROM concurrency_holders
		  WHERE key = ? AND superseded = 0 AND lease_expires_at > ?`,
		key, nowNS,
	).Scan(&holderMinNull); err != nil {
		return nil, err
	}
	holderMin := 0
	if holderMinNull.Valid && holderMinNull.Int64 > 0 {
		holderMin = int(holderMinNull.Int64)
	}
	used, err := txSumActiveCost(ctx, tx, key, nowNS)
	if err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+waiterColumns+`
		   FROM concurrency_waiters
		  WHERE key = ? AND policy IN (?, ?)
		  ORDER BY arrived_at ASC`,
		key, OnLimitQueue, OnLimitCancelOthers,
	)
	if err != nil {
		return nil, err
	}
	var candidates []ConcurrencyWaiter
	for rows.Next() {
		w, serr := scanWaiter(rows)
		if serr != nil {
			_ = rows.Close()
			return nil, serr
		}
		candidates = append(candidates, w)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Promote FIFO. Each candidate's ceiling is the minimum of the
	// already-admitted holder caps (holderMin) and its own declared
	// capacity -- most-restrictive-wins over admitted participants only.
	// As a candidate is admitted, its own cap joins holderMin so it
	// constrains the rest of the pass. A head that doesn't fit stops the
	// pass; a cheaper waiter behind it never jumps ahead.
	var promoted []ConcurrencyWaiter
	for _, w := range candidates {
		c := w.Cost
		if c <= 0 {
			c = 1
		}
		candCap := w.DeclaredCapacity
		if candCap <= 0 {
			candCap = entryCap
		}
		if holderMin > 0 && holderMin < candCap {
			candCap = holderMin
		}
		if !fitsBudget(used, c, candCap) {
			break // FIFO head doesn't fit; don't let a cheaper waiter jump it.
		}
		used += c
		if holderMin == 0 || candCap < holderMin {
			holderMin = candCap
		}
		promoted = append(promoted, w)
	}

	for i, w := range promoted {
		newHolder := w.HolderID
		if newHolder == "" {
			newHolder = fmt.Sprintf("%s/%s", w.RunID, nodeIDOrDash(w.NodeID))
		}
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
			w.Key, w.RunID, w.NodeID,
		); err != nil {
			return nil, err
		}
		c := w.Cost
		if c <= 0 {
			c = 1
		}
		// Never mint a zero-cap holder: a legacy waiter with no declared
		// capacity inherits the entry capacity, so it stays visible to the
		// effective-capacity floor.
		dc := w.DeclaredCapacity
		if dc <= 0 {
			dc = entryCap
		}
		// A promoted holder_id can still own a superseded row (a
		// CancelOthers eviction not yet reaped); clear it so the insert
		// doesn't hit the UNIQUE constraint. A live (non-superseded) row is
		// left intact, so a genuine double-promotion still surfaces.
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ? AND superseded = 1`,
			w.Key, newHolder,
		); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO concurrency_holders
			   (key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded, cost, declared_capacity)
			 VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`,
			w.Key, newHolder, w.RunID, w.NodeID, nowNS, expiresNS, c, dc,
		); err != nil {
			return nil, err
		}
		promoted[i].HolderID = newHolder
	}
	return promoted, nil
}

// HeartbeatConcurrencySlot extends the lease and reports the
// supersede signal; ErrLockHeld when caller no longer holds.
func (s *Store) HeartbeatConcurrencySlot(ctx context.Context, key, holderID string, lease time.Duration) (expires time.Time, superseded bool, err error) {
	if lease <= 0 {
		lease = DefaultConcurrencyLease
	}
	now := time.Now()
	expires = now.Add(lease)

	tx, err := s.beginTx(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var superInt int
	var leaseNS int64
	err = tx.QueryRowContext(
		ctx,
		`SELECT superseded, lease_expires_at FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	).Scan(&superInt, &leaseNS)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, ErrLockHeld
	}
	if err != nil {
		return time.Time{}, false, err
	}
	// A heartbeat that lands after the lease already expired must NOT
	// revive the holder: admission may have freed and reassigned that
	// budget once the lease lapsed, so reviving would put two live
	// holders on the same key. The reaper deletes expired rows; until it
	// does, treat the lease as lost.
	if leaseNS <= now.UnixNano() {
		return time.Time{}, false, ErrLockHeld
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE concurrency_holders SET lease_expires_at = ? WHERE key = ? AND holder_id = ?`,
		expires.UnixNano(), key, holderID,
	); err != nil {
		return time.Time{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, false, err
	}
	return expires, superInt == 1, nil
}

// ReleaseConcurrencySlot removes the holder and writes a cache entry
// when applicable. Waiter promotion runs in the caller (ReleaseAndNotify).
func (s *Store) ReleaseConcurrencySlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) (bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var runID, nodeID string
	err = tx.QueryRowContext(
		ctx,
		`SELECT run_id, node_id FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	).Scan(&runID, &nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	); err != nil {
		return false, err
	}

	if outcome == "success" && cacheKeyHash != "" && ttl > 0 {
		now := time.Now()
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO concurrency_cache
			   (key, cache_key_hash, output_ref, origin_run_id, origin_node_id, created_at, expires_at, last_hit_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (key, cache_key_hash) DO UPDATE SET
			   output_ref     = excluded.output_ref,
			   origin_run_id  = excluded.origin_run_id,
			   origin_node_id = excluded.origin_node_id,
			   created_at     = excluded.created_at,
			   expires_at     = excluded.expires_at,
			   last_hit_at    = excluded.last_hit_at`,
			key, cacheKeyHash, outputRef, runID, nodeID,
			now.UnixNano(), now.Add(ttl).UnixNano(), now.UnixNano(),
		); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// ReleaseAndNotify atomically performs release + coalesce-resolve +
// promote-next in one txn so a crash can't strand waiters.
func (s *Store) ReleaseAndNotify(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl, promoteLease time.Duration) (released bool, followers, promoted []ConcurrencyWaiter, err error) {
	if promoteLease <= 0 {
		promoteLease = DefaultConcurrencyLease
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Look up the holder to get (runID, nodeID) before deleting.
	var runID, nodeID string
	err = tx.QueryRowContext(
		ctx,
		`SELECT run_id, node_id FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	).Scan(&runID, &nodeID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Holder gone (reaped or duplicate release). Still run promote
		// to unblock waiters; followers stay parked until a real release.
		released = false
	case err != nil:
		return false, nil, nil, err
	default:
		released = true
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
			key, holderID,
		); err != nil {
			return false, nil, nil, err
		}
		if outcome == "success" && cacheKeyHash != "" && ttl > 0 {
			now := time.Now()
			if _, err := tx.ExecContext(
				ctx,
				`INSERT INTO concurrency_cache
				   (key, cache_key_hash, output_ref, origin_run_id, origin_node_id, created_at, expires_at, last_hit_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT (key, cache_key_hash) DO UPDATE SET
				   output_ref     = excluded.output_ref,
				   origin_run_id  = excluded.origin_run_id,
				   origin_node_id = excluded.origin_node_id,
				   created_at     = excluded.created_at,
				   expires_at     = excluded.expires_at,
				   last_hit_at    = excluded.last_hit_at`,
				key, cacheKeyHash, outputRef, runID, nodeID,
				now.UnixNano(), now.Add(ttl).UnixNano(), now.UnixNano(),
			); err != nil {
				return false, nil, nil, err
			}
		}

		// 2. Resolve coalesce followers in the same tx.
		frows, err := tx.QueryContext(
			ctx,
			`SELECT key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity
			   FROM concurrency_waiters
			  WHERE key = ? AND policy = ? AND leader_run_id = ? AND leader_node_id = ?
			  ORDER BY arrived_at ASC`,
			key, OnLimitCoalesce, runID, nodeID,
		)
		if err != nil {
			return false, nil, nil, err
		}
		for frows.Next() {
			w, serr := scanWaiter(frows)
			if serr != nil {
				_ = frows.Close()
				return false, nil, nil, serr
			}
			followers = append(followers, w)
		}
		_ = frows.Close()
		if err := frows.Err(); err != nil {
			return false, nil, nil, err
		}
		for _, w := range followers {
			if _, err := tx.ExecContext(
				ctx,
				`DELETE FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
				w.Key, w.RunID, w.NodeID,
			); err != nil {
				return false, nil, nil, err
			}
		}
	}

	// 3. Promote queue / cancel_others waiters against the open budget.
	// If the key was never declared (no entries row) and nothing is
	// live, there is nothing to promote.
	var hasEntry int
	err = tx.QueryRowContext(
		ctx,
		`SELECT 1 FROM concurrency_entries WHERE key = ?`, key,
	).Scan(&hasEntry)
	if errors.Is(err, sql.ErrNoRows) {
		return released, followers, nil, tx.Commit()
	}
	if err != nil {
		return false, nil, nil, err
	}
	now := time.Now()
	nowNS := now.UnixNano()
	expiresNS := now.Add(promoteLease).UnixNano()
	promoted, err = txPromoteWaiters(ctx, tx, key, nowNS, expiresNS)
	if err != nil {
		return false, nil, nil, err
	}

	if err := tx.Commit(); err != nil {
		return false, nil, nil, err
	}
	return released, followers, promoted, nil
}

// ResolveCoalesceFollowers drains coalesce waiters whose leader
// matches.
func (s *Store) ResolveCoalesceFollowers(ctx context.Context, key, leaderRunID, leaderNodeID string) ([]ConcurrencyWaiter, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(
		ctx,
		`SELECT key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity
		   FROM concurrency_waiters
		  WHERE key = ? AND policy = ? AND leader_run_id = ? AND leader_node_id = ?
		  ORDER BY arrived_at ASC`,
		key, OnLimitCoalesce, leaderRunID, leaderNodeID,
	)
	if err != nil {
		return nil, err
	}
	var out []ConcurrencyWaiter
	for rows.Next() {
		w, err := scanWaiter(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, w)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, w := range out {
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
			w.Key, w.RunID, w.NodeID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// PromoteNextWaiters grants holder rows to FIFO queue/cancel-others
// waiters up to capacity. Coalesce waiters resolve via the leader path.
func (s *Store) PromoteNextWaiters(ctx context.Context, key string, lease time.Duration) ([]ConcurrencyWaiter, error) {
	if lease <= 0 {
		lease = DefaultConcurrencyLease
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var hasEntry int
	err = tx.QueryRowContext(
		ctx,
		`SELECT 1 FROM concurrency_entries WHERE key = ?`, key,
	).Scan(&hasEntry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}

	now := time.Now()
	nowNS := now.UnixNano()
	expiresNS := now.Add(lease).UnixNano()
	promote, err := txPromoteWaiters(ctx, tx, key, nowNS, expiresNS)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return promote, nil
}

// WaiterStatus is what a polling waiter observes.
type WaiterStatus string

const (
	WaiterStillWaiting WaiterStatus = "still_waiting"
	WaiterPromoted     WaiterStatus = "promoted"
	WaiterCached       WaiterStatus = "cached"

	// WaiterLeaderFinished: the coalesce-style waiter's leader
	// completed but no cache entry was written (no CacheKey was set,
	// or leader failed). Caller looks up the leader's node row for
	// terminal outcome + output bytes.
	WaiterLeaderFinished WaiterStatus = "leader_finished"

	WaiterCancelled WaiterStatus = "cancelled"
)

// WaiterResolution: fields populate per Status.
type WaiterResolution struct {
	Status             WaiterStatus
	HolderID           string
	HolderLeaseExpires time.Time
	OutputRef          string
	OriginRunID        string
	OriginNodeID       string
	LeaderRunID        string
	LeaderNodeID       string
	// LeaderOutcome is the leader node's terminal outcome, populated on
	// WaiterLeaderFinished so a coalesced follower inherits the leader's
	// actual node result (a Skipped/Failed leader must not stamp the
	// follower Success). Empty when the leader row is gone.
	LeaderOutcome string

	// Position and Holders are populated on WaiterStillWaiting (queue
	// policy) so a poller can refresh its "N ahead, held by X" display
	// against the fully-committed queue -- self-correcting any stale
	// value computed at insert time under simultaneous arrival.
	Position int
	Holders  []ConcurrencyHolder
}

// ResolveWaiter is the read-side for polling; never inserts waiter
// rows. cacheKeyHash="" disables memo lookup; leader_* empty for
// queue/cancel_others waiters.
func (s *Store) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string) (WaiterResolution, error) {
	now := time.Now()
	nowNS := now.UnixNano()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return WaiterResolution{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Holder row present + not superseded -> Promoted.
	var holderID string
	var leaseNS int64
	var superInt int
	err = tx.QueryRowContext(
		ctx,
		`SELECT holder_id, lease_expires_at, superseded
		   FROM concurrency_holders
		  WHERE key = ? AND run_id = ? AND node_id = ?`,
		key, runID, nodeID,
	).Scan(&holderID, &leaseNS, &superInt)
	switch {
	case err == nil && superInt == 0:
		if err := tx.Commit(); err != nil {
			return WaiterResolution{}, err
		}
		return WaiterResolution{
			Status:             WaiterPromoted,
			HolderID:           holderID,
			HolderLeaseExpires: time.Unix(0, leaseNS),
		}, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return WaiterResolution{}, err
	}

	// 2. Cache hit on our hash -> Cached.
	if cacheKeyHash != "" {
		var outputRef, originRun, originNode string
		var expiresNS int64
		err := tx.QueryRowContext(
			ctx,
			`SELECT output_ref, origin_run_id, origin_node_id, expires_at
			   FROM concurrency_cache
			  WHERE key = ? AND cache_key_hash = ?`,
			key, cacheKeyHash,
		).Scan(&outputRef, &originRun, &originNode, &expiresNS)
		if err == nil && expiresNS > nowNS {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE concurrency_cache SET last_hit_at = ? WHERE key = ? AND cache_key_hash = ?`,
				nowNS, key, cacheKeyHash,
			); err != nil {
				return WaiterResolution{}, err
			}
			if err := tx.Commit(); err != nil {
				return WaiterResolution{}, err
			}
			return WaiterResolution{
				Status:       WaiterCached,
				OutputRef:    outputRef,
				OriginRunID:  originRun,
				OriginNodeID: originNode,
			}, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return WaiterResolution{}, err
		}
	}

	// 3. Waiter row still present -> keep waiting.
	var waiterArrivedNS int64
	err = tx.QueryRowContext(
		ctx,
		`SELECT arrived_at FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
		key, runID, nodeID,
	).Scan(&waiterArrivedNS)
	if err == nil {
		// Recompute position against the now-fully-committed queue, plus
		// the current holders, for the poller's live display.
		var position int
		if e := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE key = ? AND policy = ? AND arrived_at < ?`,
			key, OnLimitQueue, waiterArrivedNS,
		).Scan(&position); e != nil {
			return WaiterResolution{}, e
		}
		holders, e := txActiveHolders(ctx, tx, key, nowNS)
		if e != nil {
			return WaiterResolution{}, e
		}
		if err := tx.Commit(); err != nil {
			return WaiterResolution{}, err
		}
		return WaiterResolution{Status: WaiterStillWaiting, Position: position, Holders: holders}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WaiterResolution{}, err
	}

	// 4. Leader released, no cache entry; follower inherits the leader's
	// node outcome. The leader wrote no cache row, so it did not succeed
	// (only a successful release caches); carry its terminal outcome so
	// the follower doesn't go green for work that was skipped or failed.
	if leaderRunID != "" {
		var leaderOutcome string
		err := tx.QueryRowContext(
			ctx,
			`SELECT outcome FROM nodes WHERE run_id = ? AND node_id = ?`,
			leaderRunID, leaderNodeID,
		).Scan(&leaderOutcome)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return WaiterResolution{}, err
		}
		if err := tx.Commit(); err != nil {
			return WaiterResolution{}, err
		}
		return WaiterResolution{
			Status:        WaiterLeaderFinished,
			LeaderRunID:   leaderRunID,
			LeaderNodeID:  leaderNodeID,
			LeaderOutcome: leaderOutcome,
		}, nil
	}

	// 5. Fallthrough: request was cancelled or reaped.
	if err := tx.Commit(); err != nil {
		return WaiterResolution{}, err
	}
	return WaiterResolution{Status: WaiterCancelled}, nil
}

// CancelWaiter removes one waiter row; returns whether one matched.
func (s *Store) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	res, err := s.exec(
		ctx,
		`DELETE FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
		key, runID, nodeID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// GetConcurrencyState returns capacity + holders + waiters; ErrNotFound when undeclared.
func (s *Store) GetConcurrencyState(ctx context.Context, key string) (*ConcurrencyState, error) {
	var capacity int
	err := s.queryRow(
		ctx,
		`SELECT capacity FROM concurrency_entries WHERE key = ?`, key,
	).Scan(&capacity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	state := &ConcurrencyState{Key: key, Capacity: capacity, EffectiveCapacity: capacity}

	nowNS := time.Now().UnixNano()
	hrows, err := s.query(
		ctx,
		`SELECT key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded, cost, declared_capacity
		   FROM concurrency_holders WHERE key = ? ORDER BY claimed_at ASC`, key,
	)
	if err != nil {
		return nil, err
	}
	// minDeclared tracks the most-restrictive declared capacity over the
	// admitted holders only, mirroring the admission path's
	// effective-capacity rule. Parked waiters are excluded: a
	// non-admitted waiter holds no budget, so folding its declaration
	// would report a capacity below what the live holders actually
	// enforce (and a negative Available).
	minDeclared := 0
	noteDeclared := func(c int) {
		if c > 0 && (minDeclared == 0 || c < minDeclared) {
			minDeclared = c
		}
	}
	for hrows.Next() {
		var h ConcurrencyHolder
		var claimedNS, expiresNS int64
		var superInt int
		if err := hrows.Scan(&h.Key, &h.HolderID, &h.RunID, &h.NodeID, &claimedNS, &expiresNS, &superInt, &h.Cost, &h.DeclaredCapacity); err != nil {
			_ = hrows.Close()
			return nil, err
		}
		h.ClaimedAt = time.Unix(0, claimedNS)
		h.LeaseExpiresAt = time.Unix(0, expiresNS)
		h.Superseded = superInt == 1
		if !h.Superseded && expiresNS > nowNS {
			state.UsedCost += h.Cost
			noteDeclared(h.DeclaredCapacity)
		}
		state.Holders = append(state.Holders, h)
	}
	_ = hrows.Close()
	if err := hrows.Err(); err != nil {
		return nil, err
	}

	wrows, err := s.query(
		ctx,
		`SELECT key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity
		   FROM concurrency_waiters WHERE key = ? ORDER BY arrived_at ASC`, key,
	)
	if err != nil {
		return nil, err
	}
	for wrows.Next() {
		w, err := scanWaiter(wrows)
		if err != nil {
			_ = wrows.Close()
			return nil, err
		}
		state.Waiters = append(state.Waiters, w)
	}
	_ = wrows.Close()
	if err := wrows.Err(); err != nil {
		return nil, err
	}
	// Derive each queue-policy waiter's rank in arrival order. Waiters
	// are already ordered by arrived_at ASC.
	qrank := 0
	for i := range state.Waiters {
		if state.Waiters[i].Policy == OnLimitQueue {
			state.Waiters[i].Position = qrank
			qrank++
		}
	}
	if minDeclared > 0 {
		state.EffectiveCapacity = minDeclared
	}
	return state, nil
}

// reapStaleConcurrencyHolders deletes lease-expired holders; caller
// runs PromoteNextWaiters and emits audit events. The transaction
// holds the read locks (FOR UPDATE SKIP LOCKED on Postgres) for the
// duration so concurrent reapers pick disjoint rows.
func (s *Store) reapStaleConcurrencyHolders(ctx context.Context) ([]ConcurrencyHolder, error) {
	now := time.Now().UnixNano()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(
		ctx,
		`SELECT key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded
		   FROM concurrency_holders WHERE lease_expires_at <= ?`+s.forUpdateSkipLocked(), now,
	)
	if err != nil {
		return nil, err
	}
	var stale []ConcurrencyHolder
	for rows.Next() {
		var h ConcurrencyHolder
		var claimedNS, expiresNS int64
		var superInt int
		if err := rows.Scan(&h.Key, &h.HolderID, &h.RunID, &h.NodeID, &claimedNS, &expiresNS, &superInt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		h.ClaimedAt = time.Unix(0, claimedNS)
		h.LeaseExpiresAt = time.Unix(0, expiresNS)
		h.Superseded = superInt == 1
		stale = append(stale, h)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range stale {
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`, h.Key, h.HolderID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stale, nil
}

// ForceReleaseSupersededHolders drops superseded=1 rows so a stuck
// CancelOthers eviction can't block forward progress.
func (s *Store) ForceReleaseSupersededHolders(ctx context.Context, key string) ([]ConcurrencyHolder, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(
		ctx,
		`SELECT key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded
		   FROM concurrency_holders WHERE key = ? AND superseded = 1`, key,
	)
	if err != nil {
		return nil, err
	}
	var out []ConcurrencyHolder
	for rows.Next() {
		var h ConcurrencyHolder
		var claimedNS, expiresNS int64
		var superInt int
		if err := rows.Scan(&h.Key, &h.HolderID, &h.RunID, &h.NodeID, &claimedNS, &expiresNS, &superInt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		h.ClaimedAt = time.Unix(0, claimedNS)
		h.LeaseExpiresAt = time.Unix(0, expiresNS)
		h.Superseded = true
		out = append(out, h)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range out {
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
			h.Key, h.HolderID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// reapStaleConcurrencyWaiters drops orphan coalesce followers (leader
// gone) and any waiter older than maxAge.
func (s *Store) reapStaleConcurrencyWaiters(ctx context.Context, maxAge time.Duration) ([]ConcurrencyWaiter, error) {
	if maxAge <= 0 {
		return nil, nil
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	nowNS := time.Now().UnixNano()
	cutoff := time.Now().Add(-maxAge).UnixNano()

	// Pass 1: orphan coalesce followers (no live leader holder).
	orphanRows, err := tx.QueryContext(
		ctx,
		`SELECT w.key, w.run_id, w.node_id, w.holder_id, w.arrived_at, w.policy,
		        w.cache_key_hash, w.leader_run_id, w.leader_node_id, w.cancel_timeout_ns, w.cost, w.declared_capacity
		   FROM concurrency_waiters w
		  WHERE w.policy = ?
		    AND w.leader_run_id <> ''
		    AND NOT EXISTS (
		      SELECT 1 FROM concurrency_holders h
		       WHERE h.key = w.key
		         AND h.run_id = w.leader_run_id
		         AND h.node_id = w.leader_node_id
		         AND h.superseded = 0
		         AND h.lease_expires_at > ?
		    )`+s.forUpdateSkipLocked(),
		OnLimitCoalesce, nowNS,
	)
	if err != nil {
		return nil, err
	}
	var dropped []ConcurrencyWaiter
	for orphanRows.Next() {
		w, err := scanWaiter(orphanRows)
		if err != nil {
			_ = orphanRows.Close()
			return nil, err
		}
		dropped = append(dropped, w)
	}
	_ = orphanRows.Close()
	if err := orphanRows.Err(); err != nil {
		return nil, err
	}

	// Pass 2: anything older than maxAge.
	ageRows, err := tx.QueryContext(
		ctx,
		`SELECT key, run_id, node_id, holder_id, arrived_at, policy,
		        cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity
		   FROM concurrency_waiters WHERE arrived_at < ?`+s.forUpdateSkipLocked(),
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	// Dedupe against pass 1.
	already := make(map[string]bool, len(dropped))
	for _, d := range dropped {
		already[d.Key+"|"+d.RunID+"|"+d.NodeID] = true
	}
	for ageRows.Next() {
		w, err := scanWaiter(ageRows)
		if err != nil {
			_ = ageRows.Close()
			return nil, err
		}
		if !already[w.Key+"|"+w.RunID+"|"+w.NodeID] {
			dropped = append(dropped, w)
		}
	}
	_ = ageRows.Close()
	if err := ageRows.Err(); err != nil {
		return nil, err
	}

	for _, w := range dropped {
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
			w.Key, w.RunID, w.NodeID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dropped, nil
}

// reconcileConcurrencyKeys is the startup recovery sweep; PromoteNext
// for every key with queued waiters and room.
func (s *Store) reconcileConcurrencyKeys(ctx context.Context, lease time.Duration) (int, error) {
	if lease <= 0 {
		lease = DefaultConcurrencyLease
	}
	rows, err := s.query(
		ctx,
		`SELECT DISTINCT key FROM concurrency_waiters
		  WHERE policy IN (?, ?)`,
		OnLimitQueue, OnLimitCancelOthers,
	)
	if err != nil {
		return 0, err
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			_ = rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	total := 0
	for _, k := range keys {
		promoted, err := s.PromoteNextWaiters(ctx, k, lease)
		if err != nil {
			return total, fmt.Errorf("reconcile key %q: %w", k, err)
		}
		total += len(promoted)
	}
	return total, nil
}

// sweepExpiredConcurrencyCache removes cache entries past their TTL.
func (s *Store) sweepExpiredConcurrencyCache(ctx context.Context) (int64, error) {
	res, err := s.exec(ctx,
		`DELETE FROM concurrency_cache WHERE expires_at <= ?`, time.Now().UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// sweepLRUConcurrencyCache evicts oldest until row count == keepCount.
func (s *Store) sweepLRUConcurrencyCache(ctx context.Context, keepCount int) (int64, error) {
	if keepCount <= 0 {
		return 0, nil
	}
	var count int
	if err := s.queryRow(
		ctx,
		`SELECT COUNT(*) FROM concurrency_cache`,
	).Scan(&count); err != nil {
		return 0, err
	}
	if count <= keepCount {
		return 0, nil
	}
	evict := count - keepCount
	// (key, cache_key_hash) is the primary key -- using it as the
	// IN selector is portable across SQLite and Postgres, where
	// SQLite's `rowid` and Postgres's `ctid` would otherwise need
	// dialect branching.
	res, err := s.exec(
		ctx,
		`DELETE FROM concurrency_cache
		  WHERE (key, cache_key_hash) IN (
		    SELECT key, cache_key_hash FROM concurrency_cache
		    ORDER BY last_hit_at ASC LIMIT ?
		  )`, evict,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountConcurrencyCache is exposed for ops dashboards.
func (s *Store) CountConcurrencyCache(ctx context.Context) (int, error) {
	var n int
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM concurrency_cache`).Scan(&n)
	return n, err
}

func scanWaiter(rs rowScanner) (ConcurrencyWaiter, error) {
	var w ConcurrencyWaiter
	var arrivedNS, cancelNS int64
	if err := rs.Scan(&w.Key, &w.RunID, &w.NodeID, &w.HolderID, &arrivedNS, &w.Policy,
		&w.CacheKeyHash, &w.LeaderRunID, &w.LeaderNodeID, &cancelNS, &w.Cost, &w.DeclaredCapacity); err != nil {
		return ConcurrencyWaiter{}, err
	}
	w.ArrivedAt = time.Unix(0, arrivedNS)
	w.CancelTimeout = time.Duration(cancelNS)
	return w, nil
}

// waiterColumns is the canonical SELECT column list for scanWaiter, in
// the exact order it scans. Centralized so the cost / declared_capacity
// tail can't drift between call sites.
const waiterColumns = `key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity`

func nodeIDOrDash(nodeID string) string {
	if strings.TrimSpace(nodeID) == "" {
		return "-"
	}
	return nodeID
}
