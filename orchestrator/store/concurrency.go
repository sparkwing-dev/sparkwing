package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OnLimit policies; mirror sparkwing.OnLimitPolicy.
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

// DefaultConcurrencyHeartbeatTimeout: strictly less than the interval
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
}

// ConcurrencyState: capacity is current Max; rows are oldest-first.
type ConcurrencyState struct {
	Key      string
	Capacity int
	Holders  []ConcurrencyHolder
	Waiters  []ConcurrencyWaiter
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
	if req.Lease <= 0 {
		req.Lease = DefaultConcurrencyLease
	}
	if req.CancelTimeout <= 0 {
		req.CancelTimeout = DefaultCancelTimeout
	}

	now := time.Now()
	nowNS := now.UnixNano()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	defer tx.Rollback()

	// 1. Cache lookup; atomic with the rest so we never double-run.
	if req.CacheKeyHash != "" {
		var outputRef, originRun, originNode string
		var expiresNS int64
		err := tx.QueryRowContext(ctx,
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
				if _, err := tx.ExecContext(ctx,
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
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM concurrency_cache WHERE key = ? AND cache_key_hash = ? AND expires_at <= ?`,
				req.Key, req.CacheKeyHash, nowNS,
			); err != nil {
				return AcquireSlotResponse{}, err
			}
		}
	}

	// 2. Upsert entry (latest-wins on capacity; drift note on change).
	driftNote := ""
	prevCap := 0
	var existingCap int
	err = tx.QueryRowContext(ctx,
		`SELECT capacity FROM concurrency_entries WHERE key = ?`, req.Key,
	).Scan(&existingCap)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO concurrency_entries
			   (key, capacity, previous_capacity, last_write_run_id, last_write_node_id, updated_at)
			 VALUES (?, ?, NULL, ?, ?, ?)`,
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
				req.Key, existingCap, req.Capacity)
			if _, err := tx.ExecContext(ctx,
				`UPDATE concurrency_entries
				    SET capacity = ?, previous_capacity = ?, last_write_run_id = ?, last_write_node_id = ?, updated_at = ?
				  WHERE key = ?`,
				req.Capacity, existingCap, req.RunID, req.NodeID, nowNS, req.Key,
			); err != nil {
				return AcquireSlotResponse{}, err
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				`UPDATE concurrency_entries
				    SET last_write_run_id = ?, last_write_node_id = ?, updated_at = ?
				  WHERE key = ?`,
				req.RunID, req.NodeID, nowNS, req.Key,
			); err != nil {
				return AcquireSlotResponse{}, err
			}
		}
	}

	// 3a. Idempotent re-acquire by same holder_id; refreshes lease.
	var existingLeaseNS int64
	var existingSuperInt int
	err = tx.QueryRowContext(ctx,
		`SELECT lease_expires_at, superseded FROM concurrency_holders
		  WHERE key = ? AND holder_id = ?`,
		req.Key, req.HolderID,
	).Scan(&existingLeaseNS, &existingSuperInt)
	if err == nil && existingSuperInt == 0 {
		newExpires := now.Add(req.Lease).UnixNano()
		if _, err := tx.ExecContext(ctx,
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

	// 3. Count active, non-superseded holders.
	var activeCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM concurrency_holders
		  WHERE key = ? AND superseded = 0 AND lease_expires_at > ?`,
		req.Key, nowNS,
	).Scan(&activeCount); err != nil {
		return AcquireSlotResponse{}, err
	}

	// 4. Slot available -> grant immediately.
	if activeCount < req.Capacity {
		expiresNS := now.Add(req.Lease).UnixNano()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO concurrency_holders
			   (key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded)
			 VALUES (?, ?, ?, ?, ?, ?, 0)`,
			req.Key, req.HolderID, req.RunID, req.NodeID, nowNS, expiresNS,
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
		err := tx.QueryRowContext(ctx,
			`SELECT run_id, node_id FROM concurrency_holders
			  WHERE key = ? AND superseded = 0
			  ORDER BY claimed_at ASC LIMIT 1`,
			req.Key,
		).Scan(&leaderRun, &leaderNode)
		if err != nil {
			return AcquireSlotResponse{}, fmt.Errorf("coalesce: select leader: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO concurrency_waiters
			   (key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			req.Key, req.RunID, req.NodeID, req.HolderID, nowNS, OnLimitCoalesce, req.CacheKeyHash, leaderRun, leaderNode,
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
		toSupersede := max(activeCount+1-req.Capacity, 1)
		rows, err := tx.QueryContext(ctx,
			`SELECT holder_id FROM concurrency_holders
			  WHERE key = ? AND superseded = 0
			  ORDER BY claimed_at ASC LIMIT ?`,
			req.Key, toSupersede,
		)
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		var supersededIDs []string
		for rows.Next() {
			var hid string
			if err := rows.Scan(&hid); err != nil {
				rows.Close()
				return AcquireSlotResponse{}, err
			}
			supersededIDs = append(supersededIDs, hid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return AcquireSlotResponse{}, err
		}
		for _, hid := range supersededIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE concurrency_holders SET superseded = 1 WHERE key = ? AND holder_id = ?`,
				req.Key, hid,
			); err != nil {
				return AcquireSlotResponse{}, err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO concurrency_waiters
			   (key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns)
			 VALUES (?, ?, ?, ?, ?, ?, ?, '', '', ?)`,
			req.Key, req.RunID, req.NodeID, req.HolderID, nowNS, OnLimitCancelOthers, req.CacheKeyHash, int64(req.CancelTimeout),
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
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO concurrency_waiters
			   (key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns)
			 VALUES (?, ?, ?, ?, ?, ?, ?, '', '', 0)`,
			req.Key, req.RunID, req.NodeID, req.HolderID, nowNS, OnLimitQueue, req.CacheKeyHash,
		); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := tx.Commit(); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{Kind: AcquireQueued, PreviousCapacity: prevCap, DriftNote: driftNote}, nil
	}
}

// HeartbeatConcurrencySlot extends the lease and reports the
// supersede signal; ErrLockHeld when caller no longer holds.
func (s *Store) HeartbeatConcurrencySlot(ctx context.Context, key, holderID string, lease time.Duration) (expires time.Time, superseded bool, err error) {
	if lease <= 0 {
		lease = DefaultConcurrencyLease
	}
	now := time.Now()
	expires = now.Add(lease)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, false, err
	}
	defer tx.Rollback()

	var superInt int
	err = tx.QueryRowContext(ctx,
		`SELECT superseded FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	).Scan(&superInt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, ErrLockHeld
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if _, err := tx.ExecContext(ctx,
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var runID, nodeID string
	err = tx.QueryRowContext(ctx,
		`SELECT run_id, node_id FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	).Scan(&runID, &nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	); err != nil {
		return false, err
	}

	if outcome == "success" && cacheKeyHash != "" && ttl > 0 {
		now := time.Now()
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO concurrency_cache
			   (key, cache_key_hash, output_ref, origin_run_id, origin_node_id, created_at, expires_at, last_hit_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
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
func (s *Store) ReleaseAndNotify(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl, promoteLease time.Duration) (released bool, followers []ConcurrencyWaiter, promoted []ConcurrencyWaiter, err error) {
	if promoteLease <= 0 {
		promoteLease = DefaultConcurrencyLease
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, nil, err
	}
	defer tx.Rollback()

	// 1. Look up the holder to get (runID, nodeID) before deleting.
	var runID, nodeID string
	err = tx.QueryRowContext(ctx,
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
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
			key, holderID,
		); err != nil {
			return false, nil, nil, err
		}
		if outcome == "success" && cacheKeyHash != "" && ttl > 0 {
			now := time.Now()
			if _, err := tx.ExecContext(ctx,
				`INSERT OR REPLACE INTO concurrency_cache
				   (key, cache_key_hash, output_ref, origin_run_id, origin_node_id, created_at, expires_at, last_hit_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				key, cacheKeyHash, outputRef, runID, nodeID,
				now.UnixNano(), now.Add(ttl).UnixNano(), now.UnixNano(),
			); err != nil {
				return false, nil, nil, err
			}
		}

		// 2. Resolve coalesce followers in the same tx.
		frows, err := tx.QueryContext(ctx,
			`SELECT key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns
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
				frows.Close()
				return false, nil, nil, serr
			}
			followers = append(followers, w)
		}
		frows.Close()
		if err := frows.Err(); err != nil {
			return false, nil, nil, err
		}
		for _, w := range followers {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
				w.Key, w.RunID, w.NodeID,
			); err != nil {
				return false, nil, nil, err
			}
		}
	}

	// 3. Promote queue / cancel_others waiters up to capacity.
	var capacity int
	err = tx.QueryRowContext(ctx,
		`SELECT capacity FROM concurrency_entries WHERE key = ?`, key,
	).Scan(&capacity)
	if errors.Is(err, sql.ErrNoRows) {
		return released, followers, nil, tx.Commit()
	}
	if err != nil {
		return false, nil, nil, err
	}
	now := time.Now()
	nowNS := now.UnixNano()
	var activeCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM concurrency_holders
		  WHERE key = ? AND superseded = 0 AND lease_expires_at > ?`,
		key, nowNS,
	).Scan(&activeCount); err != nil {
		return false, nil, nil, err
	}
	openSlots := capacity - activeCount
	if openSlots > 0 {
		prows, err := tx.QueryContext(ctx,
			`SELECT key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns
			   FROM concurrency_waiters
			  WHERE key = ? AND policy IN (?, ?)
			  ORDER BY arrived_at ASC LIMIT ?`,
			key, OnLimitQueue, OnLimitCancelOthers, openSlots,
		)
		if err != nil {
			return false, nil, nil, err
		}
		for prows.Next() {
			w, serr := scanWaiter(prows)
			if serr != nil {
				prows.Close()
				return false, nil, nil, serr
			}
			promoted = append(promoted, w)
		}
		prows.Close()
		if err := prows.Err(); err != nil {
			return false, nil, nil, err
		}
		expiresNS := now.Add(promoteLease).UnixNano()
		for i, w := range promoted {
			newHolder := w.HolderID
			if newHolder == "" {
				newHolder = fmt.Sprintf("%s/%s", w.RunID, nodeIDOrDash(w.NodeID))
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
				w.Key, w.RunID, w.NodeID,
			); err != nil {
				return false, nil, nil, err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO concurrency_holders
				   (key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded)
				 VALUES (?, ?, ?, ?, ?, ?, 0)`,
				w.Key, newHolder, w.RunID, w.NodeID, nowNS, expiresNS,
			); err != nil {
				return false, nil, nil, err
			}
			promoted[i].HolderID = newHolder
		}
	}

	if err := tx.Commit(); err != nil {
		return false, nil, nil, err
	}
	return released, followers, promoted, nil
}

// ResolveCoalesceFollowers drains coalesce waiters whose leader
// matches.
func (s *Store) ResolveCoalesceFollowers(ctx context.Context, key, leaderRunID, leaderNodeID string) ([]ConcurrencyWaiter, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns
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
			rows.Close()
			return nil, err
		}
		out = append(out, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, w := range out {
		if _, err := tx.ExecContext(ctx,
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var capacity int
	err = tx.QueryRowContext(ctx,
		`SELECT capacity FROM concurrency_entries WHERE key = ?`, key,
	).Scan(&capacity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}

	now := time.Now()
	nowNS := now.UnixNano()

	var activeCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM concurrency_holders
		  WHERE key = ? AND superseded = 0 AND lease_expires_at > ?`,
		key, nowNS,
	).Scan(&activeCount); err != nil {
		return nil, err
	}

	openSlots := capacity - activeCount
	if openSlots <= 0 {
		return nil, tx.Commit()
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns
		   FROM concurrency_waiters
		  WHERE key = ? AND policy IN (?, ?)
		  ORDER BY arrived_at ASC LIMIT ?`,
		key, OnLimitQueue, OnLimitCancelOthers, openSlots,
	)
	if err != nil {
		return nil, err
	}
	var promote []ConcurrencyWaiter
	for rows.Next() {
		w, err := scanWaiter(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		promote = append(promote, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	expiresNS := now.Add(lease).UnixNano()
	for i, w := range promote {
		holderID := w.HolderID
		if holderID == "" {
			// Pre-fix waiter row; fall back to "runID/nodeID".
			holderID = fmt.Sprintf("%s/%s", w.RunID, nodeIDOrDash(w.NodeID))
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
			w.Key, w.RunID, w.NodeID,
		); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO concurrency_holders
			   (key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded)
			 VALUES (?, ?, ?, ?, ?, ?, 0)`,
			w.Key, holderID, w.RunID, w.NodeID, nowNS, expiresNS,
		); err != nil {
			return nil, err
		}
		promote[i].HolderID = holderID
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
}

// ResolveWaiter is the read-side for polling; never inserts waiter
// rows. cacheKeyHash="" disables memo lookup; leader_* empty for
// queue/cancel_others waiters.
func (s *Store) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string) (WaiterResolution, error) {
	now := time.Now()
	nowNS := now.UnixNano()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WaiterResolution{}, err
	}
	defer tx.Rollback()

	// 1. Holder row present + not superseded -> Promoted.
	var holderID string
	var leaseNS int64
	var superInt int
	err = tx.QueryRowContext(ctx,
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
		err := tx.QueryRowContext(ctx,
			`SELECT output_ref, origin_run_id, origin_node_id, expires_at
			   FROM concurrency_cache
			  WHERE key = ? AND cache_key_hash = ?`,
			key, cacheKeyHash,
		).Scan(&outputRef, &originRun, &originNode, &expiresNS)
		if err == nil && expiresNS > nowNS {
			if _, err := tx.ExecContext(ctx,
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
	err = tx.QueryRowContext(ctx,
		`SELECT arrived_at FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
		key, runID, nodeID,
	).Scan(&waiterArrivedNS)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return WaiterResolution{}, err
		}
		return WaiterResolution{Status: WaiterStillWaiting}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WaiterResolution{}, err
	}

	// 4. Leader released, no cache entry; follower inherits outcome.
	if leaderRunID != "" {
		if err := tx.Commit(); err != nil {
			return WaiterResolution{}, err
		}
		return WaiterResolution{
			Status:       WaiterLeaderFinished,
			LeaderRunID:  leaderRunID,
			LeaderNodeID: leaderNodeID,
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
	res, err := s.db.ExecContext(ctx,
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
	err := s.db.QueryRowContext(ctx,
		`SELECT capacity FROM concurrency_entries WHERE key = ?`, key,
	).Scan(&capacity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	state := &ConcurrencyState{Key: key, Capacity: capacity}

	hrows, err := s.db.QueryContext(ctx,
		`SELECT key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded
		   FROM concurrency_holders WHERE key = ? ORDER BY claimed_at ASC`, key,
	)
	if err != nil {
		return nil, err
	}
	for hrows.Next() {
		var h ConcurrencyHolder
		var claimedNS, expiresNS int64
		var superInt int
		if err := hrows.Scan(&h.Key, &h.HolderID, &h.RunID, &h.NodeID, &claimedNS, &expiresNS, &superInt); err != nil {
			hrows.Close()
			return nil, err
		}
		h.ClaimedAt = time.Unix(0, claimedNS)
		h.LeaseExpiresAt = time.Unix(0, expiresNS)
		h.Superseded = superInt == 1
		state.Holders = append(state.Holders, h)
	}
	hrows.Close()
	if err := hrows.Err(); err != nil {
		return nil, err
	}

	wrows, err := s.db.QueryContext(ctx,
		`SELECT key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns
		   FROM concurrency_waiters WHERE key = ? ORDER BY arrived_at ASC`, key,
	)
	if err != nil {
		return nil, err
	}
	for wrows.Next() {
		w, err := scanWaiter(wrows)
		if err != nil {
			wrows.Close()
			return nil, err
		}
		state.Waiters = append(state.Waiters, w)
	}
	wrows.Close()
	if err := wrows.Err(); err != nil {
		return nil, err
	}
	return state, nil
}

// ReapStaleConcurrencyHolders deletes lease-expired holders; caller
// runs PromoteNextWaiters and emits audit events.
func (s *Store) ReapStaleConcurrencyHolders(ctx context.Context) ([]ConcurrencyHolder, error) {
	now := time.Now().UnixNano()
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, holder_id, run_id, node_id, claimed_at, lease_expires_at, superseded
		   FROM concurrency_holders WHERE lease_expires_at <= ?`, now,
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
			rows.Close()
			return nil, err
		}
		h.ClaimedAt = time.Unix(0, claimedNS)
		h.LeaseExpiresAt = time.Unix(0, expiresNS)
		h.Superseded = superInt == 1
		stale = append(stale, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range stale {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`, h.Key, h.HolderID,
		); err != nil {
			return nil, err
		}
	}
	return stale, nil
}

// ForceReleaseSupersededHolders drops superseded=1 rows so a stuck
// CancelOthers eviction can't block forward progress.
func (s *Store) ForceReleaseSupersededHolders(ctx context.Context, key string) ([]ConcurrencyHolder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
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
			rows.Close()
			return nil, err
		}
		h.ClaimedAt = time.Unix(0, claimedNS)
		h.LeaseExpiresAt = time.Unix(0, expiresNS)
		h.Superseded = true
		out = append(out, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range out {
		if _, err := tx.ExecContext(ctx,
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

// ReapStaleConcurrencyWaiters drops orphan coalesce followers (leader
// gone) and any waiter older than maxAge.
func (s *Store) ReapStaleConcurrencyWaiters(ctx context.Context, maxAge time.Duration) ([]ConcurrencyWaiter, error) {
	if maxAge <= 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	nowNS := time.Now().UnixNano()
	cutoff := time.Now().Add(-maxAge).UnixNano()

	// Pass 1: orphan coalesce followers (no live leader holder).
	orphanRows, err := tx.QueryContext(ctx,
		`SELECT w.key, w.run_id, w.node_id, w.holder_id, w.arrived_at, w.policy,
		        w.cache_key_hash, w.leader_run_id, w.leader_node_id, w.cancel_timeout_ns
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
		    )`,
		OnLimitCoalesce, nowNS,
	)
	if err != nil {
		return nil, err
	}
	var dropped []ConcurrencyWaiter
	for orphanRows.Next() {
		w, err := scanWaiter(orphanRows)
		if err != nil {
			orphanRows.Close()
			return nil, err
		}
		dropped = append(dropped, w)
	}
	orphanRows.Close()
	if err := orphanRows.Err(); err != nil {
		return nil, err
	}

	// Pass 2: anything older than maxAge.
	ageRows, err := tx.QueryContext(ctx,
		`SELECT key, run_id, node_id, holder_id, arrived_at, policy,
		        cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns
		   FROM concurrency_waiters WHERE arrived_at < ?`,
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
			ageRows.Close()
			return nil, err
		}
		if !already[w.Key+"|"+w.RunID+"|"+w.NodeID] {
			dropped = append(dropped, w)
		}
	}
	ageRows.Close()
	if err := ageRows.Err(); err != nil {
		return nil, err
	}

	for _, w := range dropped {
		if _, err := tx.ExecContext(ctx,
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

// ReconcileConcurrencyKeys is the startup recovery sweep; PromoteNext
// for every key with queued waiters and room.
func (s *Store) ReconcileConcurrencyKeys(ctx context.Context, lease time.Duration) (int, error) {
	if lease <= 0 {
		lease = DefaultConcurrencyLease
	}
	rows, err := s.db.QueryContext(ctx,
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
			rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	rows.Close()
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

// SweepExpiredConcurrencyCache removes cache entries past their TTL.
func (s *Store) SweepExpiredConcurrencyCache(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM concurrency_cache WHERE expires_at <= ?`, time.Now().UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SweepLRUConcurrencyCache evicts oldest until row count == keepCount.
func (s *Store) SweepLRUConcurrencyCache(ctx context.Context, keepCount int) (int64, error) {
	if keepCount <= 0 {
		return 0, nil
	}
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM concurrency_cache`,
	).Scan(&count); err != nil {
		return 0, err
	}
	if count <= keepCount {
		return 0, nil
	}
	evict := count - keepCount
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM concurrency_cache
		  WHERE ROWID IN (
		    SELECT ROWID FROM concurrency_cache
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
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_cache`).Scan(&n)
	return n, err
}

func scanWaiter(rs rowScanner) (ConcurrencyWaiter, error) {
	var w ConcurrencyWaiter
	var arrivedNS, cancelNS int64
	if err := rs.Scan(&w.Key, &w.RunID, &w.NodeID, &w.HolderID, &arrivedNS, &w.Policy,
		&w.CacheKeyHash, &w.LeaderRunID, &w.LeaderNodeID, &cancelNS); err != nil {
		return ConcurrencyWaiter{}, err
	}
	w.ArrivedAt = time.Unix(0, arrivedNS)
	w.CancelTimeout = time.Duration(cancelNS)
	return w, nil
}

func nodeIDOrDash(nodeID string) string {
	if strings.TrimSpace(nodeID) == "" {
		return "-"
	}
	return nodeID
}
