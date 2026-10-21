package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func fitsBudget(used, cost, capacity int) bool {
	return used <= capacity && cost <= capacity-used
}

func holderLiveSQL(alias string) string {
	return alias + "superseded = 0 AND " + alias + "lease_expires_at > ?"
}

func holderCountsForBudget(superseded bool, leaseExpiresNS, nowNS int64) bool {
	return !superseded && leaseExpiresNS > nowNS
}

func declaredCapacityFloorTerm(declared int) int {
	if declared > 0 {
		return declared
	}
	return 1
}

// OnLimit policies control admission when capacity is exhausted.
const (
	OnLimitQueue        = "queue"
	OnLimitSkip         = "skip"
	OnLimitFail         = "fail"
	OnLimitCancelOthers = "cancel_others"
)

// DefaultConcurrencyLease is DefaultLeaseDuration by construction so
// holder and claim leases can't drift apart; a holder must be silent
// for the full window to be reaped.
const DefaultConcurrencyLease = DefaultLeaseDuration

// DefaultCancelTimeout bounds CancelOthers eviction.
const DefaultCancelTimeout = 60 * time.Second

// AcquireKind covers user-selectable + non-selectable outcomes.
type AcquireKind string

const (
	AcquireGranted          AcquireKind = "granted"
	AcquireQueued           AcquireKind = "queued"
	AcquireSkipped          AcquireKind = "skipped"
	AcquireFailed           AcquireKind = "failed"
	AcquireCancellingOthers AcquireKind = "cancelling_others"
)

// AcquireSlotRequest selects a group and the work consuming its capacity.
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
	// Position is the number of queue-policy waiters that arrived earlier,
	// QueueLength is the total queued for the key, and Holders are the slots
	// currently held. Weighted admission may backfill a later waiter that fits
	// while an earlier waiter stays queued. Zero/empty for other kinds.
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
	QueueArrivedAt time.Time
	LeaseExpiresAt time.Time
	Superseded     bool

	// Cost is the admission weight this holder draws from the key's
	// budget; DeclaredCapacity is the capacity it declared. Populated by
	// GetConcurrencyState for the operator budget view; zero on the hot
	// acquire/promote paths that don't need them.
	Cost             int
	DeclaredCapacity int

	// safety: unexported because the team is not part of what a caller
	// names -- a concurrency key is authored in pipeline YAML and scoped
	// by the handle that reads it -- while a fleet-wide reaper still has
	// to know which team's row it just read to delete the right one.
	team Team
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

	// Position is the waiter's 0-based arrival rank among queue-policy
	// waiters for the key, as derived by GetConcurrencyState. Weighted
	// admission may backfill later waiters that fit ahead of earlier waiters
	// that do not. Zero for non-queue waiters.
	Position int

	// safety: unexported for the same reason as ConcurrencyHolder.team.
	team       Team
	generation int64
}

// safety: a bare key no longer identifies a row, and a fleet-wide sweep
// touching several teams' keys carries the pair through to the invariant
// check.
type concurrencyKey struct {
	team Team
	key  string
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

func (t *Tenant) acquireConcurrencySlot(ctx context.Context, req AcquireSlotRequest, fence *capacityFence) (AcquireSlotResponse, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	defer rollbackOrLog(tx)
	result, err := t.acquireConcurrencySlotTx(ctx, tx, req, fence, time.Time{})
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	if err := txCommitChecked(ctx, tx, tx.time.UnixNano(), concurrencyKey{t.team, req.Key}); err != nil {
		return AcquireSlotResponse{}, err
	}
	return result, nil
}

func (t *Tenant) acquireConcurrencySlotTx(ctx context.Context, tx *storeTx, req AcquireSlotRequest, fence *capacityFence, deadline time.Time) (AcquireSlotResponse, error) {
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

	if req.Cost > req.Capacity {
		if req.Policy == OnLimitSkip {
			return AcquireSlotResponse{Kind: AcquireSkipped}, nil
		}
		return AcquireSlotResponse{Kind: AcquireFailed}, nil
	}

	var err error

	if fence != nil {
		if err := prepareCapacityAcquisitionTx(ctx, tx, t.team, req.Key, *fence); err != nil {
			return AcquireSlotResponse{}, err
		}
	}

	if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, req.Key); err != nil {
		return AcquireSlotResponse{}, err
	}

	if err := checkCapacityMutationTx(ctx, tx, t.team, req.Key, req.HolderID, fence); err != nil {
		return AcquireSlotResponse{}, err
	}

	// safety: read the clock only after the tx holds the write lock; a
	// pre-BEGIN timestamp goes stale while waiting and revives expired
	// holders whose budget was already reassigned.
	now := tx.time
	if !deadline.IsZero() {
		req.Lease = slotLease(now, deadline, req.Lease)
	}
	if req.Lease <= 0 {
		return AcquireSlotResponse{}, holderRefusal()
	}
	nowNS := now.UnixNano()

	if reaped, err := txReapTerminalConcurrencyHolders(ctx, tx, t.team, req.Key); err != nil {
		return AcquireSlotResponse{}, err
	} else if len(reaped) > 0 {
		if _, err := txPromoteWaitersLocked(ctx, tx, t.team, req.Key, nowNS, now.Add(req.Lease).UnixNano(), livePollingWaiter{}); err != nil {
			return AcquireSlotResponse{}, err
		}
	}

	driftNote := ""
	prevCap := 0
	var existingCap int
	err = tx.QueryRowContext(
		ctx,
		`SELECT capacity FROM concurrency_entries WHERE team = ? AND key = ?`+t.s.forUpdate(), string(t.team), req.Key,
	).Scan(&existingCap)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO concurrency_entries
			   (team, key, capacity, previous_capacity, last_write_run_id, last_write_node_id, updated_at)
			 VALUES (?, ?, ?, NULL, ?, ?, ?)
			 ON CONFLICT (team, key) DO NOTHING`,
			string(t.team), req.Key, req.Capacity, req.RunID, req.NodeID, nowNS,
		); err != nil {
			return AcquireSlotResponse{}, err
		}
		// safety: a conflict-losing insert holds no row lock; re-read FOR
		// UPDATE so two first-acquires of a key serialize like later pairs.
		var relockCap int
		if err := tx.QueryRowContext(
			ctx,
			`SELECT capacity FROM concurrency_entries WHERE team = ? AND key = ?`+t.s.forUpdate(), string(t.team), req.Key,
		).Scan(&relockCap); err != nil && !errors.Is(err, sql.ErrNoRows) {
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
				  WHERE team = ? AND key = ?`,
				req.Capacity, existingCap, req.RunID, req.NodeID, nowNS, string(t.team), req.Key,
			); err != nil {
				return AcquireSlotResponse{}, err
			}
		} else {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE concurrency_entries
				    SET last_write_run_id = ?, last_write_node_id = ?, updated_at = ?
				  WHERE team = ? AND key = ?`,
				req.RunID, req.NodeID, nowNS, string(t.team), req.Key,
			); err != nil {
				return AcquireSlotResponse{}, err
			}
		}
	}

	var existingLeaseNS int64
	var existingSuperInt int
	err = tx.QueryRowContext(
		ctx,
		`SELECT lease_expires_at, superseded FROM concurrency_holders
		  WHERE team = ? AND key = ? AND holder_id = ?`,
		string(t.team), req.Key, req.HolderID,
	).Scan(&existingLeaseNS, &existingSuperInt)
	if err == nil && holderCountsForBudget(existingSuperInt == 1, existingLeaseNS, nowNS) {
		newExpires := now.Add(req.Lease).UnixNano()
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE concurrency_holders SET lease_expires_at = ? WHERE team = ? AND key = ? AND holder_id = ?`,
			newExpires, string(t.team), req.Key, req.HolderID,
		); err != nil {
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

	acct, err := txConcurrencyAccounting(ctx, tx, t.team, req.Key, nowNS)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	activeCost := acct.used
	effCap := acct.effectiveCapacity(req.Capacity)

	policy := req.Policy

	queueBlocked := false
	if policy == OnLimitQueue {
		blocked, err := txEarlierRunnableQueueWaiter(ctx, tx, t.team, req.Key, req.RunID, req.NodeID, nowNS, acct)
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		queueBlocked = blocked
	}
	if !queueBlocked && fitsBudget(activeCost, req.Cost, effCap) {
		expiresNS := now.Add(req.Lease).UnixNano()
		if err := txInsertHolder(ctx, tx, holderRow{
			team: t.team, key: req.Key, holderID: req.HolderID, runID: req.RunID, nodeID: req.NodeID,
			cost: req.Cost, declaredCapacity: req.Capacity,
		}, nowNS, expiresNS); err != nil {
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

	switch policy {
	case OnLimitSkip:
		return AcquireSlotResponse{Kind: AcquireSkipped, PreviousCapacity: prevCap, DriftNote: driftNote}, nil

	case OnLimitFail:
		return AcquireSlotResponse{Kind: AcquireFailed, PreviousCapacity: prevCap, DriftNote: driftNote}, nil

	case OnLimitCancelOthers:
		var supersededIDs []string
		freed := 0
		for _, h := range acct.holders {
			if fitsBudget(activeCost-freed, req.Cost, effCap) && len(supersededIDs) > 0 {
				break
			}
			supersededIDs = append(supersededIDs, h.HolderID)
			freed += max(h.Cost, 1)
		}
		for _, id := range supersededIDs {
			if err := txSupersede(ctx, tx, t.team, req.Key, id); err != nil {
				return AcquireSlotResponse{}, err
			}
			if fence != nil {
				if err := cancelSupersededCapacityTx(ctx, tx, t.team, req.Key, id, fence.actor); err != nil {
					return AcquireSlotResponse{}, err
				}
			}
		}
		expiresNS := now.Add(req.Lease).UnixNano()
		if err := txInsertHolder(ctx, tx, holderRow{
			team: t.team, key: req.Key, holderID: req.HolderID, runID: req.RunID, nodeID: req.NodeID,
			cost: req.Cost, declaredCapacity: req.Capacity,
		}, nowNS, expiresNS); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{
			Kind:             AcquireCancellingOthers,
			HolderID:         req.HolderID,
			LeaseExpiresAt:   time.Unix(0, expiresNS),
			SupersededIDs:    supersededIDs,
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
		}, nil

	case OnLimitQueue:
		fallthrough
	default:
		arrivedNS := nowNS
		if queuedArrivedNS, queued, err := txQueueWaiterArrivedAt(ctx, tx, t.team, req.Key, req.RunID, req.NodeID); err != nil {
			return AcquireSlotResponse{}, err
		} else if queued {
			arrivedNS = queuedArrivedNS
		} else if arrivedNS, err = txNextQueueArrival(ctx, tx, t.team, req.Key, nowNS); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := txPark(ctx, tx, t.team, ConcurrencyWaiter{
			Key: req.Key, RunID: req.RunID, NodeID: req.NodeID, HolderID: req.HolderID,
			Policy: OnLimitQueue, CacheKeyHash: req.CacheKeyHash,
			Cost: req.Cost, DeclaredCapacity: req.Capacity,
		}, arrivedNS, now.Add(req.Lease).UnixNano()); err != nil {
			return AcquireSlotResponse{}, err
		}
		var position, queueLen int
		if err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE team = ? AND key = ? AND policy = ? AND arrived_at < ?`,
			string(t.team), req.Key, OnLimitQueue, arrivedNS,
		).Scan(&position); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE team = ? AND key = ? AND policy = ?`,
			string(t.team), req.Key, OnLimitQueue,
		).Scan(&queueLen); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{
			Kind:             AcquireQueued,
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
			Position:         position,
			QueueLength:      queueLen,
			Holders:          acct.holders,
		}, nil
	}
}

func txActiveHolders(ctx context.Context, tx *storeTx, team Team, key string, nowNS int64) ([]ConcurrencyHolder, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders
		  WHERE team = ? AND key = ? AND `+holderLiveSQL("")+`
		  ORDER BY claimed_at ASC`,
		string(team), key, nowNS,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConcurrencyHolder
	for rows.Next() {
		h, err := scanHolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

type concurrencyAccounting struct {
	used int

	floor int

	entryCap int

	holders []ConcurrencyHolder
}

func (a concurrencyAccounting) effectiveCapacity(incoming int) int {
	eff := a.floor
	if incoming > 0 && (eff == 0 || incoming < eff) {
		eff = incoming
	}
	if eff > 0 {
		return eff
	}
	return a.entryCap
}

func txConcurrencyAccounting(ctx context.Context, tx *storeTx, team Team, key string, nowNS int64) (concurrencyAccounting, error) {
	entryCap, err := txEntryCapacity(ctx, tx, team, key)
	if err != nil {
		return concurrencyAccounting{}, err
	}
	holders, err := txActiveHolders(ctx, tx, team, key, nowNS)
	if err != nil {
		return concurrencyAccounting{}, err
	}
	a := concurrencyAccounting{entryCap: entryCap, holders: holders}
	for _, h := range holders {
		a.used += max(h.Cost, 1)
		if t := declaredCapacityFloorTerm(h.DeclaredCapacity); a.floor == 0 || t < a.floor {
			a.floor = t
		}
	}
	return a, nil
}

func txEarlierRunnableQueueWaiter(ctx context.Context, tx *storeTx, team Team, key, runID, nodeID string, nowNS int64, acct concurrencyAccounting) (bool, error) {
	currentArrivedNS, currentQueued, err := txQueueWaiterArrivedAt(ctx, tx, team, key, runID, nodeID)
	if err != nil {
		return false, err
	}

	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+waiterColumns+`
		   FROM concurrency_waiters
		  WHERE team = ? AND key = ? AND policy = ? AND (run_id != ? OR node_id != ?) AND lease_expires_at > ?
		  ORDER BY arrived_at ASC`,
		string(team), key, OnLimitQueue, runID, nodeID, nowNS,
	)
	if err != nil {
		return false, err
	}
	var waiters []ConcurrencyWaiter
	for rows.Next() {
		w, serr := scanWaiter(rows)
		if serr != nil {
			_ = rows.Close()
			return false, serr
		}
		waiters = append(waiters, w)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	ids := make([]string, 0, len(waiters))
	for _, w := range waiters {
		ids = append(ids, w.RunID)
	}
	live, err := txLiveCapacityRunIDs(ctx, tx, team, ids, nowNS)
	if err != nil {
		return false, err
	}

	used := acct.used
	holderMin := acct.floor
	for _, w := range waiters {
		current, err := capacityWaiterCurrentTx(ctx, tx, team, w)
		if err != nil {
			return false, err
		}
		if !current {
			continue
		}
		deadline, err := capacityQueueDeadlineTx(ctx, tx, team, w)
		if err != nil {
			return false, err
		}
		if !deadline.IsZero() && !deadline.After(tx.time) {
			continue
		}
		if currentQueued && w.ArrivedAt.UnixNano() >= currentArrivedNS {
			continue
		}
		if !live[w.RunID] {
			if _, derr := txDeleteWaiter(ctx, tx, w.team, w.Key, w.RunID, w.NodeID); derr != nil {
				return false, derr
			}
			continue
		}
		cost, capacity := waiterBudget(w, acct.entryCap, holderMin)
		if fitsBudget(used, cost, capacity) {
			return true, nil
		}
		if blockedByYoungerHolders(acct.holders, w.ArrivedAt.UnixNano(), cost, capacity) {
			return true, nil
		}
	}
	return false, nil
}

// safety: promotion orders waiters by arrived_at, and a wall clock that
// steps backwards (WSL2 time sync, NTP, another replica's skew) would stamp
// a new arrival ahead of older ones. The caller holds the entries row lock,
// so the key's newest stamp cannot move under this read.
func txNextQueueArrival(ctx context.Context, tx *storeTx, team Team, key string, nowNS int64) (int64, error) {
	var newest sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT MAX(arrived_at) FROM concurrency_waiters WHERE team = ? AND key = ?`,
		string(team), key,
	).Scan(&newest); err != nil {
		return 0, err
	}
	if newest.Valid && newest.Int64 >= nowNS {
		return newest.Int64 + 1, nil
	}
	return nowNS, nil
}

func txQueueWaiterArrivedAt(ctx context.Context, tx *storeTx, team Team, key, runID, nodeID string) (int64, bool, error) {
	var arrivedNS int64
	err := tx.QueryRowContext(
		ctx,
		`SELECT arrived_at FROM concurrency_waiters
		  WHERE team = ? AND key = ? AND run_id = ? AND node_id = ? AND policy = ? AND lease_expires_at > ?`,
		string(team), key, runID, nodeID, OnLimitQueue, tx.time.UnixNano(),
	).Scan(&arrivedNS)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return arrivedNS, true, nil
}

func waiterBudget(w ConcurrencyWaiter, entryCap, holderMin int) (int, int) {
	cost := w.Cost
	if cost <= 0 {
		cost = 1
	}
	capacity := w.DeclaredCapacity
	if capacity <= 0 {
		capacity = entryCap
	}
	if holderMin > 0 && holderMin < capacity {
		capacity = holderMin
	}
	return cost, capacity
}

func blockedByYoungerHolders(holders []ConcurrencyHolder, waiterArrivedNS int64, cost, capacity int) bool {
	usedWithoutYounger := 0
	for _, h := range holders {
		if holderAdmissionOrderNS(h) > waiterArrivedNS {
			continue
		}
		usedWithoutYounger += max(h.Cost, 1)
	}
	return fitsBudget(usedWithoutYounger, cost, capacity)
}

func holderAdmissionOrderNS(h ConcurrencyHolder) int64 {
	if !h.QueueArrivedAt.IsZero() {
		return h.QueueArrivedAt.UnixNano()
	}
	return h.ClaimedAt.UnixNano()
}

func txLockEntry(ctx context.Context, tx *storeTx, forUpdate string, team Team, key string) error {
	var one int
	err := tx.QueryRowContext(
		ctx,
		`SELECT 1 FROM concurrency_entries WHERE team = ? AND key = ?`+forUpdate, string(team), key,
	).Scan(&one)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return tx.capacityLocked(ctx)
}

// Lock waits can outlive holders whose capacity a later transaction reused.
func (tx *storeTx) capacityLocked(ctx context.Context) error {
	now, err := tx.now(ctx)
	if err != nil {
		return err
	}
	tx.time = now
	return nil
}

func txEntryCapacity(ctx context.Context, tx *storeTx, team Team, key string) (int, error) {
	var entryCap int
	err := tx.QueryRowContext(
		ctx, `SELECT capacity FROM concurrency_entries WHERE team = ? AND key = ?`, string(team), key,
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

func txPromoteWaiters(ctx context.Context, tx *storeTx, team Team, key string, nowNS, expiresNS int64) ([]ConcurrencyWaiter, error) {
	if _, err := txReapTerminalConcurrencyHolders(ctx, tx, team, key); err != nil {
		return nil, err
	}
	return txPromoteWaitersLocked(ctx, tx, team, key, nowNS, expiresNS, livePollingWaiter{})
}

func txPromotePollingWaiter(ctx context.Context, tx *storeTx, team Team, key, runID, nodeID string, nowNS, expiresNS int64) ([]ConcurrencyWaiter, error) {
	if _, err := txReapTerminalConcurrencyHolders(ctx, tx, team, key); err != nil {
		return nil, err
	}
	return txPromoteWaitersLocked(ctx, tx, team, key, nowNS, expiresNS, livePollingWaiter{runID: runID, nodeID: nodeID})
}

type livePollingWaiter struct {
	runID  string
	nodeID string
}

func (p livePollingWaiter) matches(waiter ConcurrencyWaiter) bool {
	return p.runID != "" && waiter.RunID == p.runID && waiter.NodeID == p.nodeID
}

func txPromoteWaitersLocked(ctx context.Context, tx *storeTx, team Team, key string, nowNS, expiresNS int64, polling livePollingWaiter) ([]ConcurrencyWaiter, error) {
	acct, err := txConcurrencyAccounting(ctx, tx, team, key, nowNS)
	if err != nil {
		return nil, err
	}
	entryCap, holderMin, used := acct.entryCap, acct.floor, acct.used

	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+waiterColumns+`
		   FROM concurrency_waiters
		  WHERE team = ? AND key = ? AND policy IN (?, ?) AND lease_expires_at > ?
		  ORDER BY arrived_at ASC`,
		string(team), key, OnLimitQueue, OnLimitCancelOthers, nowNS,
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

	ids := make([]string, 0, len(candidates))
	for _, w := range candidates {
		ids = append(ids, w.RunID)
	}
	live, err := txLiveCapacityRunIDs(ctx, tx, team, ids, nowNS)
	if err != nil {
		return nil, err
	}

	var promoted []ConcurrencyWaiter
	for _, w := range candidates {
		current, err := capacityWaiterCurrentTx(ctx, tx, team, w)
		if err != nil {
			return nil, err
		}
		if !current {
			continue
		}
		deadline, err := capacityQueueDeadlineTx(ctx, tx, team, w)
		if err != nil {
			return nil, err
		}
		if !deadline.IsZero() && !deadline.After(tx.time) {
			continue
		}
		if !live[w.RunID] && !polling.matches(w) {
			if _, derr := txDeleteWaiter(ctx, tx, w.team, w.Key, w.RunID, w.NodeID); derr != nil {
				return nil, derr
			}
			continue
		}
		c, candCap := waiterBudget(w, entryCap, holderMin)
		if !fitsBudget(used, c, candCap) {
			if blockedByYoungerHolders(acct.holders, w.ArrivedAt.UnixNano(), c, candCap) {
				break
			}
			continue
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
		if _, err := txDeleteWaiter(ctx, tx, w.team, w.Key, w.RunID, w.NodeID); err != nil {
			return nil, err
		}
		c := w.Cost
		if c <= 0 {
			c = 1
		}
		dc := w.DeclaredCapacity
		if dc <= 0 {
			dc = entryCap
		}
		if err := txInsertHolder(ctx, tx, holderRow{
			team: w.team, key: w.Key, holderID: newHolder, runID: w.RunID, nodeID: w.NodeID,
			cost: c, declaredCapacity: dc, queueArrivedNS: w.ArrivedAt.UnixNano(),
		}, nowNS, expiresNS); err != nil {
			return nil, err
		}
		promoted[i].HolderID = newHolder
	}
	return promoted, nil
}

func txLiveCapacityRunIDs(ctx context.Context, tx *storeTx, team Team, ids []string, nowNS int64) (map[string]bool, error) {
	seen := make(map[string]bool, len(ids))
	args := make([]any, 0, len(ids)+3)
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		args = append(args, id)
	}
	if len(args) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(args))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	args = append(args, team, nowNS, nowNS, nowNS)
	rows, err := tx.QueryContext(
		ctx,
		`SELECT r.id FROM runs r
		  WHERE r.id IN (`+strings.Join(placeholders, ",")+`)
		    AND r.team = ? AND `+capacityRunLiveSQL,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	live := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	return live, rows.Err()
}

func txCommitChecked(ctx context.Context, tx *storeTx, nowNS int64, keys ...concurrencyKey) error {
	seen := make(map[concurrencyKey]bool, len(keys))
	for _, k := range keys {
		if k.key == "" || seen[k] {
			continue
		}
		seen[k] = true
		if err := txCheckConcurrencyInvariants(ctx, tx, k.team, k.key, nowNS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func txCheckConcurrencyInvariants(ctx context.Context, tx *storeTx, team Team, key string, nowNS int64) error {
	acct, err := txConcurrencyAccounting(ctx, tx, team, key, nowNS)
	if err != nil {
		return err
	}
	var violations []string
	if eff := acct.effectiveCapacity(0); acct.used > eff {
		violations = append(violations, fmt.Sprintf("live cost %d exceeds effective capacity %d", acct.used, eff))
	}
	for _, h := range acct.holders {
		if h.DeclaredCapacity > 0 && max(h.Cost, 1) > h.DeclaredCapacity {
			violations = append(violations, fmt.Sprintf("holder %q cost %d exceeds its declared capacity %d", h.HolderID, h.Cost, h.DeclaredCapacity))
		}
	}
	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+waiterColumns+` FROM concurrency_waiters WHERE team = ? AND key = ?`, string(team), key,
	)
	if err != nil {
		return err
	}
	var waiters []ConcurrencyWaiter
	for rows.Next() {
		w, err := scanWaiter(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		waiters = append(waiters, w)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, w := range waiters {
		switch w.Policy {
		case OnLimitQueue, OnLimitCancelOthers:
		default:
			violations = append(violations, fmt.Sprintf("waiter %s/%s has unknown policy %q", w.RunID, w.NodeID, w.Policy))
		}
		for _, h := range acct.holders {
			if h.RunID == w.RunID && h.NodeID == w.NodeID {
				violations = append(violations, fmt.Sprintf("%s/%s both holds and waits", w.RunID, w.NodeID))
			}
		}
	}
	if len(violations) == 0 {
		return nil
	}
	return fmt.Errorf("concurrency invariants violated for key %q: %s", key, strings.Join(violations, "; "))
}

type holderRow struct {
	team             Team
	key              string
	holderID         string
	runID            string
	nodeID           string
	cost             int
	declaredCapacity int
	queueArrivedNS   int64
}

func capacityLeaseCeilingTx(ctx context.Context, tx *storeTx, team Team, run, job string, expires int64) (int64, error) {
	var lease sql.NullInt64
	var err error
	if job != "" {
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(n.lease_expires_at,CASE WHEN `+controllerRunOwnership+` THEN (SELECT until FROM leases WHERE name='controller.owner') ELSE (SELECT lease_expires_at FROM triggers WHERE team=r.team AND id=r.id) END) FROM nodes n JOIN runs r ON r.team=n.team AND r.id=n.run_id WHERE n.team=? AND n.run_id=? AND n.node_id=?`, team, run, job).Scan(&lease)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT CASE WHEN `+controllerRunOwnership+` THEN (SELECT until FROM leases WHERE name='controller.owner') ELSE (SELECT lease_expires_at FROM triggers WHERE team=r.team AND id=r.id) END FROM runs r WHERE team=? AND id=?`, team, run).Scan(&lease)
	}
	if err != nil {
		return 0, err
	}
	drain, err := cancelDrainTx(ctx, tx, team, run, job)
	if err != nil {
		return 0, err
	}
	bound, err := leaseBound(tx.time, time.Unix(0, expires), time.Unix(0, lease.Int64), drain)
	if !lease.Valid {
		bound, err = leaseBound(tx.time, time.Unix(0, expires), drain)
	}
	return bound.UnixNano(), err
}

func txInsertHolder(ctx context.Context, tx *storeTx, h holderRow, nowNS, expiresNS int64) error {
	generation, err := capacityGenerationTx(ctx, tx, h.team, h.runID, h.nodeID)
	if err != nil {
		return err
	}
	expiresNS, err = capacityLeaseCeilingTx(ctx, tx, h.team, h.runID, h.nodeID, expiresNS)
	if err != nil {
		return err
	}
	if _, err := txDeleteWaiter(ctx, tx, h.team, h.key, h.runID, h.nodeID); err != nil {
		return err
	}
	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO concurrency_holders
		   (team, key, holder_id, run_id, node_id, claimed_at, queue_arrived_at, lease_expires_at, superseded, cost, declared_capacity, generation)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
		 ON CONFLICT (team, key, holder_id) DO UPDATE SET
		   run_id            = excluded.run_id,
		   node_id           = excluded.node_id,
		   claimed_at        = excluded.claimed_at,
		   queue_arrived_at  = excluded.queue_arrived_at,
		   lease_expires_at  = excluded.lease_expires_at,
		   superseded        = 0,
		   cost              = excluded.cost,
		   declared_capacity = excluded.declared_capacity,
		   generation        = excluded.generation
		 WHERE concurrency_holders.superseded = 1 OR concurrency_holders.lease_expires_at <= ?`,
		string(h.team), h.key, h.holderID, h.runID, h.nodeID, nowNS, h.queueArrivedNS, expiresNS, h.cost, h.declaredCapacity, generation, nowNS,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("concurrency: holder %q for key %q would clobber a live holder", h.holderID, h.key)
	}
	return nil
}

func txReleaseHolder(ctx context.Context, tx *storeTx, team Team, key, holderID string) (released bool, runID, nodeID string, err error) {
	var queueArrivedNS int64
	err = tx.QueryRowContext(
		ctx,
		`SELECT run_id, node_id, queue_arrived_at
		   FROM concurrency_holders WHERE team = ? AND key = ? AND holder_id = ?`,
		string(team), key, holderID,
	).Scan(&runID, &nodeID, &queueArrivedNS)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", "", nil
	}
	if err != nil {
		return false, "", "", err
	}

	now := tx.time

	if nodeID == "" && queueArrivedNS > 0 {
		if err := txSupersede(ctx, tx, team, key, holderID); err != nil {
			return false, "", "", err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE concurrency_holders
   SET lease_expires_at = ?, cost = 0
 WHERE team = ? AND key = ? AND holder_id = ?`, now.UnixNano(), string(team), key, holderID); err != nil {
			return false, "", "", err
		}
	} else {
		if err := txDeleteHolder(ctx, tx, team, key, holderID); err != nil {
			return false, "", "", err
		}
	}

	return true, runID, nodeID, nil
}

// safety: leader reads retain tenant scope before disclosing an outcome.
func txNodeOutcome(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) (outcome, failureReason string, found bool, err error) {
	err = tx.QueryRowContext(
		ctx,
		`SELECT outcome, failure_reason FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(team), runID, nodeID,
	).Scan(&outcome, &failureReason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return outcome, failureReason, true, nil
}

func txPark(ctx context.Context, tx *storeTx, team Team, w ConcurrencyWaiter, arrivedNS, expiresNS int64) error {
	generation, err := capacityGenerationTx(ctx, tx, team, w.RunID, w.NodeID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO concurrency_waiters
		   (team, key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity, generation, lease_expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (team, key, run_id, node_id) DO UPDATE SET
		   holder_id         = excluded.holder_id,
		   arrived_at        = excluded.arrived_at,
		   policy            = excluded.policy,
		   cache_key_hash    = excluded.cache_key_hash,
		   leader_run_id     = excluded.leader_run_id,
		   leader_node_id    = excluded.leader_node_id,
		   cancel_timeout_ns = excluded.cancel_timeout_ns,
		   cost              = excluded.cost,
		   declared_capacity = excluded.declared_capacity,
		   generation        = excluded.generation,
		   lease_expires_at  = excluded.lease_expires_at`,
		string(team), w.Key, w.RunID, w.NodeID, w.HolderID, arrivedNS, w.Policy, w.CacheKeyHash,
		w.LeaderRunID, w.LeaderNodeID, int64(w.CancelTimeout), w.Cost, w.DeclaredCapacity, generation, expiresNS,
	)
	return err
}

func txSupersede(ctx context.Context, tx *storeTx, team Team, key, holderID string) error {
	_, err := tx.ExecContext(
		ctx,
		`UPDATE concurrency_holders SET superseded = 1 WHERE team = ? AND key = ? AND holder_id = ?`,
		string(team), key, holderID,
	)
	return err
}

func txDeleteHolder(ctx context.Context, tx *storeTx, team Team, key, holderID string) error {
	_, err := tx.ExecContext(
		ctx,
		`DELETE FROM concurrency_holders WHERE team = ? AND key = ? AND holder_id = ?`,
		string(team), key, holderID,
	)
	return err
}

func txDeleteWaiter(ctx context.Context, tx *storeTx, team Team, key, runID, nodeID string) (bool, error) {
	res, err := tx.ExecContext(
		ctx,
		`DELETE FROM concurrency_waiters WHERE team = ? AND key = ? AND run_id = ? AND node_id = ?`,
		string(team), key, runID, nodeID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// WaiterStatus is what a polling waiter observes.
type WaiterStatus string

const (
	WaiterStillWaiting WaiterStatus = "still_waiting"
	WaiterPromoted     WaiterStatus = "promoted"

	WaiterLeaderFinished WaiterStatus = "leader_finished"

	WaiterCancelled WaiterStatus = "cancelled"
	WaiterExpired   WaiterStatus = "expired"
)

// WaiterResolution: fields populate per Status.
type WaiterResolution struct {
	Status              WaiterStatus
	HolderID            string
	HolderLeaseExpires  time.Time
	OutputRef           string
	OriginRunID         string
	OriginNodeID        string
	LeaderRunID         string
	LeaderNodeID        string
	LeaderOutcome       string
	LeaderFailureReason string

	Position    int
	QueueLength int
	Holders     []ConcurrencyHolder
}

func (t *Tenant) resolveWaiter(ctx context.Context, key, runID, nodeID, leaderRunID, leaderNodeID string, fence *capacityFence) (WaiterResolution, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return WaiterResolution{}, err
	}
	defer rollbackOrLog(tx)
	if fence != nil {
		if err := checkCapacityFenceTx(ctx, tx, t.team, *fence); err != nil {
			return WaiterResolution{}, err
		}
	}
	busy, err := tryLockEntryTx(ctx, tx, t.team, key)
	if err != nil {
		return WaiterResolution{}, err
	}
	if busy {
		// safety: a writer holds the entry, so its commit decides this waiter;
		// a poll that queued behind it would delay that writer for nothing.
		// The head of the queue still waits, because it is next to promote.
		observed, ok, err := t.observeWaiterTx(ctx, tx, key, runID, nodeID)
		if err != nil {
			return WaiterResolution{}, err
		}
		if ok && (observed.Status != WaiterStillWaiting || observed.Position > 0) {
			return observed, nil
		}
	}
	result, err := t.resolveWaiterTx(ctx, tx, key, runID, nodeID, leaderRunID, leaderNodeID, DefaultConcurrencyLease, fence)
	if err != nil {
		return WaiterResolution{}, err
	}
	if err := txCommitChecked(ctx, tx, tx.time.UnixNano(), concurrencyKey{t.team, key}); err != nil {
		return WaiterResolution{}, err
	}
	return result, nil
}

// observeWaiterTx answers a poll from committed rows without the entry lock.
// It decides only a promoted holder or a still-waiting queue entry; anything
// else falls through to the locked path.
func (t *Tenant) observeWaiterTx(ctx context.Context, tx *storeTx, key, runID, nodeID string) (WaiterResolution, bool, error) {
	nowNS := tx.time.UnixNano()
	var holderID string
	var leaseNS int64
	err := tx.QueryRowContext(ctx,
		`SELECT holder_id, lease_expires_at FROM concurrency_holders
		  WHERE team = ? AND key = ? AND run_id = ? AND node_id = ? AND `+holderLiveSQL(""),
		string(t.team), key, runID, nodeID, nowNS,
	).Scan(&holderID, &leaseNS)
	if err == nil {
		return WaiterResolution{Status: WaiterPromoted, HolderID: holderID, HolderLeaseExpires: time.Unix(0, leaseNS)}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WaiterResolution{}, false, err
	}
	waiter, err := scanWaiter(tx.QueryRowContext(ctx,
		`SELECT `+waiterColumns+` FROM concurrency_waiters WHERE team = ? AND key = ? AND run_id = ? AND node_id = ? AND lease_expires_at > ?`,
		string(t.team), key, runID, nodeID, nowNS,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return WaiterResolution{}, false, nil
	}
	if err != nil {
		return WaiterResolution{}, false, err
	}
	deadline, err := capacityQueueDeadlineTx(ctx, tx, t.team, waiter)
	if err != nil || (!deadline.IsZero() && !deadline.After(tx.time)) {
		return WaiterResolution{}, false, err
	}
	var position, queueLength int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM concurrency_waiters WHERE team = ? AND key = ? AND policy IN (?, ?) AND arrived_at < ?`,
		string(t.team), key, OnLimitQueue, OnLimitCancelOthers, waiter.ArrivedAt.UnixNano(),
	).Scan(&position); err != nil {
		return WaiterResolution{}, false, err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM concurrency_waiters WHERE team = ? AND key = ? AND policy IN (?, ?)`,
		string(t.team), key, OnLimitQueue, OnLimitCancelOthers,
	).Scan(&queueLength); err != nil {
		return WaiterResolution{}, false, err
	}
	holders, err := txActiveHolders(ctx, tx, t.team, key, nowNS)
	if err != nil {
		return WaiterResolution{}, false, err
	}
	return WaiterResolution{Status: WaiterStillWaiting, Position: position, QueueLength: queueLength, Holders: holders}, true, nil
}

func (t *Tenant) resolveWaiterTx(ctx context.Context, tx *storeTx, key, runID, nodeID, leaderRunID, leaderNodeID string, lease time.Duration, fence *capacityFence) (WaiterResolution, error) {
	var err error

	if fence != nil {
		if err := checkCapacityFenceTx(ctx, tx, t.team, *fence); err != nil {
			return WaiterResolution{}, err
		}
	}

	// safety: clock read after BEGIN so liveness answers match what the
	// serialized writers committed, not a pre-wait snapshot.
	nowNS := tx.time.UnixNano()
	if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
		return WaiterResolution{}, err
	}
	if err := checkCapacityMutationTx(ctx, tx, t.team, key, "", fence); err != nil {
		return WaiterResolution{}, err
	}
	nowNS = tx.time.UnixNano()
	if _, err := txReapTerminalConcurrencyHolders(ctx, tx, t.team, key); err != nil {
		return WaiterResolution{}, err
	}

	var holderID string
	var leaseNS int64
	var superInt int
	err = tx.QueryRowContext(
		ctx,
		`SELECT holder_id, lease_expires_at, superseded
		   FROM concurrency_holders
		  WHERE team = ? AND key = ? AND run_id = ? AND node_id = ?`,
		string(t.team), key, runID, nodeID,
	).Scan(&holderID, &leaseNS, &superInt)
	switch {
	case err == nil && superInt == 0 && leaseNS > nowNS:
		return WaiterResolution{
			Status:             WaiterPromoted,
			HolderID:           holderID,
			HolderLeaseExpires: time.Unix(0, leaseNS),
		}, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return WaiterResolution{}, err
	}

	waiter, err := scanWaiter(tx.QueryRowContext(
		ctx,
		`SELECT `+waiterColumns+` FROM concurrency_waiters WHERE team = ? AND key = ? AND run_id = ? AND node_id = ? AND lease_expires_at > ?`,
		string(t.team), key, runID, nodeID, nowNS,
	))
	if err == nil {
		deadline, err := capacityQueueDeadlineTx(ctx, tx, t.team, waiter)
		if err != nil {
			return WaiterResolution{}, err
		}
		if !deadline.IsZero() && !deadline.After(tx.time) {
			if _, err := txDeleteWaiter(ctx, tx, t.team, key, runID, nodeID); err != nil {
				return WaiterResolution{}, err
			}
			return WaiterResolution{Status: WaiterExpired}, nil
		}

		shouldPromote := waiter.Policy == OnLimitQueue
		if waiter.Policy == OnLimitCancelOthers {
			var holderCount int
			if err := tx.QueryRowContext(
				ctx,
				`SELECT COUNT(*) FROM concurrency_holders WHERE team = ? AND key = ?`,
				string(t.team), key,
			).Scan(&holderCount); err != nil {
				return WaiterResolution{}, err
			}
			shouldPromote = holderCount == 0
		}
		if shouldPromote {
			now := tx.time
			promoted, err := txPromotePollingWaiter(ctx, tx, t.team, key, runID, nodeID, now.UnixNano(), now.Add(lease).UnixNano())
			if err != nil {
				return WaiterResolution{}, err
			}
			for _, waiter := range promoted {
				if waiter.RunID == runID && waiter.NodeID == nodeID {
					return WaiterResolution{
						Status:             WaiterPromoted,
						HolderID:           waiter.HolderID,
						HolderLeaseExpires: now.Add(lease),
					}, nil
				}
			}
		}
		var position int
		if e := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE team = ? AND key = ? AND policy IN (?, ?) AND arrived_at < ?`,
			string(t.team), key, OnLimitQueue, OnLimitCancelOthers, waiter.ArrivedAt.UnixNano(),
		).Scan(&position); e != nil {
			return WaiterResolution{}, e
		}
		var queueLength int
		if e := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE team = ? AND key = ? AND policy IN (?, ?)`,
			string(t.team), key, OnLimitQueue, OnLimitCancelOthers,
		).Scan(&queueLength); e != nil {
			return WaiterResolution{}, e
		}
		holders, e := txActiveHolders(ctx, tx, t.team, key, nowNS)
		if e != nil {
			return WaiterResolution{}, e
		}
		return WaiterResolution{Status: WaiterStillWaiting, Position: position, QueueLength: queueLength, Holders: holders}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WaiterResolution{}, err
	}

	if leaderRunID != "" {
		leaderOutcome, leaderReason, found, err := txNodeOutcome(ctx, tx, t.team, leaderRunID, leaderNodeID)
		if err != nil {
			return WaiterResolution{}, err
		}
		if !found || leaderOutcome != "success" {
			return WaiterResolution{Status: WaiterCancelled}, nil
		}
		return WaiterResolution{
			Status:              WaiterLeaderFinished,
			LeaderRunID:         leaderRunID,
			LeaderNodeID:        leaderNodeID,
			LeaderOutcome:       leaderOutcome,
			LeaderFailureReason: leaderReason,
		}, nil
	}

	return WaiterResolution{Status: WaiterCancelled}, nil
}

// GetConcurrencyState returns this tenant's capacity, holders and waiters for a key.
func (t *Tenant) GetConcurrencyState(ctx context.Context, key string) (*ConcurrencyState, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	state, err := t.concurrencyStateTx(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	return state, tx.Commit()
}

func (t *Tenant) concurrencyStateTx(ctx context.Context, tx *storeTx, key string) (*ConcurrencyState, error) {
	var capacity int
	err := tx.QueryRowContext(
		ctx,
		`SELECT capacity FROM concurrency_entries WHERE team = ? AND key = ?`, string(t.team), key,
	).Scan(&capacity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("concurrency state", key)
	}
	if err != nil {
		return nil, err
	}

	state := &ConcurrencyState{Key: key, Capacity: capacity, EffectiveCapacity: capacity}

	nowNS := tx.time.UnixNano()
	hrows, err := tx.QueryContext(
		ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders WHERE team = ? AND key = ? ORDER BY claimed_at ASC`, string(t.team), key,
	)
	if err != nil {
		return nil, err
	}
	floor := 0
	for hrows.Next() {
		h, err := scanHolder(hrows)
		if err != nil {
			_ = hrows.Close()
			return nil, err
		}
		if holderCountsForBudget(h.Superseded, h.LeaseExpiresAt.UnixNano(), nowNS) {
			state.UsedCost += max(h.Cost, 1)
			if t := declaredCapacityFloorTerm(h.DeclaredCapacity); floor == 0 || t < floor {
				floor = t
			}
		}
		state.Holders = append(state.Holders, h)
	}
	_ = hrows.Close()
	if err := hrows.Err(); err != nil {
		return nil, err
	}

	wrows, err := tx.QueryContext(
		ctx,
		`SELECT `+waiterColumns+`
		   FROM concurrency_waiters WHERE team = ? AND key = ? ORDER BY arrived_at ASC`, string(t.team), key,
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
	qrank := 0
	for i := range state.Waiters {
		if state.Waiters[i].Policy == OnLimitQueue {
			state.Waiters[i].Position = qrank
			qrank++
		}
	}
	if floor > 0 {
		state.EffectiveCapacity = floor
	}
	return state, nil
}

func txReapTerminalConcurrencyHolders(ctx context.Context, tx *storeTx, team Team, key string) ([]ConcurrencyHolder, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+prefixColumns(holderColumns, "h.")+`
		   FROM concurrency_holders h
		   JOIN runs r ON r.id = h.run_id AND r.team = h.team
		  WHERE h.team = ? AND h.key = ?
		    AND r.finished_at IS NOT NULL`,
		string(team), key,
	)
	if err != nil {
		return nil, err
	}
	var stale []ConcurrencyHolder
	for rows.Next() {
		holder, err := scanHolder(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		stale = append(stale, holder)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, holder := range stale {
		if _, _, _, err := txReleaseHolder(ctx, tx, holder.team, holder.Key, holder.HolderID); err != nil {
			return nil, err
		}
	}
	return stale, nil
}

func reapStaleConcurrencyHoldersTx(ctx context.Context, tx *storeTx, team Team, key string) ([]ConcurrencyHolder, error) {
	now := tx.time.UnixNano()

	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders WHERE team=? AND key=? AND (lease_expires_at <= ? OR EXISTS (SELECT 1 FROM runs r WHERE r.team=concurrency_holders.team AND r.id=concurrency_holders.run_id AND r.finished_at IS NOT NULL))`+tx.forUpdate(), team, key, now,
	)
	if err != nil {
		return nil, err
	}
	var stale []ConcurrencyHolder
	for rows.Next() {
		h, err := scanHolder(rows)
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		stale = append(stale, h)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, h := range stale {
		if err := txDeleteHolder(ctx, tx, h.team, h.Key, h.HolderID); err != nil {
			return nil, err
		}
	}
	return stale, nil
}

func reapStaleConcurrencyWaitersTx(ctx context.Context, tx *storeTx, team Team, key string, maxAge time.Duration) ([]ConcurrencyWaiter, error) {
	now := tx.time
	ns := now.UnixNano()
	rows, err := tx.QueryContext(ctx, `SELECT `+prefixColumns(waiterColumns, "w.")+`
FROM concurrency_waiters w WHERE w.team=? AND w.key=? AND (w.lease_expires_at<=?
OR (w.arrived_at<? AND NOT EXISTS (
 SELECT 1 FROM runs r WHERE r.team=w.team AND r.id=w.run_id AND `+capacityRunLiveSQL+`))
OR EXISTS (SELECT 1 FROM runs r WHERE r.team=w.team AND r.id=w.run_id AND r.finished_at IS NOT NULL)
OR (w.node_id<>'' AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.team=w.team AND n.run_id=w.run_id AND n.node_id=w.node_id AND n.status<>'done')))`,
		team, key, ns, now.Add(-maxAge).UnixNano(), ns, ns, ns)
	if err != nil {
		return nil, err
	}
	var dropped []ConcurrencyWaiter
	for rows.Next() {
		w, err := scanWaiter(rows)
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		dropped = append(dropped, w)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, w := range dropped {
		if _, err := txDeleteWaiter(ctx, tx, w.team, w.Key, w.RunID, w.NodeID); err != nil {
			return nil, err
		}
	}
	return dropped, nil
}

func prefixColumns(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

const holderColumns = `key, holder_id, run_id, node_id, claimed_at, queue_arrived_at, lease_expires_at, superseded, cost, declared_capacity, team`

func scanHolder(rs rowScanner) (ConcurrencyHolder, error) {
	var h ConcurrencyHolder
	var claimedNS, queueArrivedNS, expiresNS int64
	var superInt int
	if err := rs.Scan(&h.Key, &h.HolderID, &h.RunID, &h.NodeID, &claimedNS, &queueArrivedNS, &expiresNS, &superInt, &h.Cost, &h.DeclaredCapacity, &h.team); err != nil {
		return ConcurrencyHolder{}, err
	}
	h.ClaimedAt = time.Unix(0, claimedNS)
	if queueArrivedNS > 0 {
		h.QueueArrivedAt = time.Unix(0, queueArrivedNS)
	}
	h.LeaseExpiresAt = time.Unix(0, expiresNS)
	h.Superseded = superInt == 1
	return h, nil
}

func scanWaiter(rs rowScanner) (ConcurrencyWaiter, error) {
	var w ConcurrencyWaiter
	var arrivedNS, cancelNS int64
	if err := rs.Scan(&w.Key, &w.RunID, &w.NodeID, &w.HolderID, &arrivedNS, &w.Policy,
		&w.CacheKeyHash, &w.LeaderRunID, &w.LeaderNodeID, &cancelNS, &w.Cost, &w.DeclaredCapacity, &w.team, &w.generation); err != nil {
		return ConcurrencyWaiter{}, err
	}
	w.ArrivedAt = time.Unix(0, arrivedNS)
	w.CancelTimeout = time.Duration(cancelNS)
	return w, nil
}

const waiterColumns = `key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity, team, generation`

func nodeIDOrDash(nodeID string) string {
	if strings.TrimSpace(nodeID) == "" {
		return "-"
	}
	return nodeID
}
