package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
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
	onLimitQueue        = "queue"
	onLimitSkip         = "skip"
	onLimitFail         = "fail"
	onLimitCancelOthers = "cancel_others"
)

// defaultConcurrencyLease bounds logical capacity between renewals. Claim or
// owner liveness retains it only within that lease and leaseBoundTx deadlines.
const defaultConcurrencyLease = DefaultLeaseDuration

// acquireSlotRequest selects a group and the work consuming its capacity.
type acquireSlotRequest struct {
	Key      string
	HolderID string
	RunID    string
	NodeID   string
	Capacity int
	Policy   string
	Lease    time.Duration

	// Cost is the admission weight this arrival draws from the key's
	// capacity (author-defined units). Zero or negative is treated as
	// 1. Admission compares the summed cost of live holders plus this
	// cost against the effective capacity, not a slot count.
	Cost int
}

// ConcurrencyHolder mirrors the concurrency_holders row.
type ConcurrencyHolder struct {
	runstate.ConcurrencyHolder
	// The fleet reaper retains the team privately to delete the right row.
	team Team
}

// ConcurrencyWaiter reports persisted membership and derived queue state.
type ConcurrencyWaiter struct {
	BlockingReason string
	Key            string
	RunID          string
	NodeID         string
	HolderID       string
	ArrivedAt      time.Time
	Policy         string

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

func (t *Tenant) acquireConcurrencySlotTx(ctx context.Context, tx *storeTx, req acquireSlotRequest, fence *capacityFence, deadline time.Time) (runstate.AcquireSlotResponse, error) {
	if fence != nil {
		if err := checkCapacityFenceTx(ctx, tx, t.team, req.Key, *fence); err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		expires, err := capacityLeaseCeilingTx(ctx, tx, t.team, req.RunID, req.NodeID, tx.now(ctx).Add(defaultConcurrencyLease).UnixNano())
		if err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		deadline = time.Unix(0, expires)
		req.Lease = defaultConcurrencyLease
		req, err = sealedCapacityRequestTx(ctx, tx, t.team, req, fence.actor.Authority)
		if err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
	}
	if req.Key == "" {
		return runstate.AcquireSlotResponse{}, errors.New("concurrency: empty key")
	}
	if req.HolderID == "" {
		return runstate.AcquireSlotResponse{}, errors.New("concurrency: empty holder_id")
	}
	if req.Policy == "" {
		req.Policy = onLimitQueue
	}
	if req.Capacity <= 0 {
		req.Capacity = 1
	}
	if req.Cost <= 0 {
		req.Cost = 1
	}
	if req.Lease <= 0 {
		req.Lease = defaultConcurrencyLease
	}

	if req.Cost > req.Capacity {
		if req.Policy == onLimitSkip {
			return runstate.AcquireSlotResponse{Kind: runstate.AcquireSkipped}, nil
		}
		return runstate.AcquireSlotResponse{Kind: runstate.AcquireFailed}, nil
	}

	var err error

	if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, req.Key); err != nil {
		return runstate.AcquireSlotResponse{}, err
	}

	if err := checkCapacityMutationTx(ctx, tx, t.team, req.Key, req.HolderID, fence); err != nil {
		return runstate.AcquireSlotResponse{}, err
	}

	now := tx.now(ctx)
	nowNS := now.UnixNano()

	driftNote := ""
	prevCap := 0
	var existingCap int
	err = tx.QueryRowContext(
		ctx,
		`SELECT capacity FROM concurrency_entries WHERE team = ? AND key = ?`+t.s.forUpdate(), string(t.team), req.Key,
	).Scan(&existingCap)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.execContext(
			ctx,
			`INSERT INTO concurrency_entries
			   (team, key, capacity, previous_capacity, last_write_run_id, last_write_node_id, updated_at)
			 VALUES (?, ?, ?, NULL, ?, ?, ?)
			 ON CONFLICT (team, key) DO NOTHING`,
			string(t.team), req.Key, req.Capacity, req.RunID, req.NodeID, nowNS,
		); err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		// safety: a conflict-losing insert holds no row lock; re-read FOR
		// UPDATE so two first-acquires of a key serialize like later pairs.
		var relockCap int
		if err := tx.QueryRowContext(
			ctx,
			`SELECT capacity FROM concurrency_entries WHERE team = ? AND key = ?`+t.s.forUpdate(), string(t.team), req.Key,
		).Scan(&relockCap); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return runstate.AcquireSlotResponse{}, err
		}
	case err != nil:
		return runstate.AcquireSlotResponse{}, err
	default:
		if existingCap != req.Capacity {
			prevCap = existingCap
			driftNote = fmt.Sprintf(
				"concurrency key %q capacity changed %d -> %d (latest-wins; previous writer was preserved in previous_capacity)",
				req.Key, existingCap, req.Capacity,
			)
			if _, err := tx.execContext(
				ctx,
				`UPDATE concurrency_entries
				    SET capacity = ?, previous_capacity = ?, last_write_run_id = ?, last_write_node_id = ?, updated_at = ?
				  WHERE team = ? AND key = ?`,
				req.Capacity, existingCap, req.RunID, req.NodeID, nowNS, string(t.team), req.Key,
			); err != nil {
				return runstate.AcquireSlotResponse{}, err
			}
		} else {
			if _, err := tx.execContext(
				ctx,
				`UPDATE concurrency_entries
				    SET last_write_run_id = ?, last_write_node_id = ?, updated_at = ?
				  WHERE team = ? AND key = ?`,
				req.RunID, req.NodeID, nowNS, string(t.team), req.Key,
			); err != nil {
				return runstate.AcquireSlotResponse{}, err
			}
		}
	}

	// A missing entry locks nothing. Create and lock it before locking holders,
	// and read fresh time after waiting so expired capacity cannot be revived.
	now = tx.now(ctx)
	nowNS = now.UnixNano()
	if !deadline.IsZero() {
		req.Lease = slotLease(now, deadline, req.Lease)
	}
	if req.Lease <= 0 {
		return runstate.AcquireSlotResponse{}, holderRefusal()
	}
	if _, err := reapStaleConcurrencyHoldersTx(ctx, tx, t.team, req.Key); err != nil {
		return runstate.AcquireSlotResponse{}, err
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
		newExpires, err := capacityLeaseCeilingTx(ctx, tx, t.team, req.RunID, req.NodeID, now.Add(req.Lease).UnixNano())
		if err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		if _, err := tx.execContext(
			ctx,
			`UPDATE concurrency_holders SET lease_expires_at = ? WHERE team = ? AND key = ? AND holder_id = ?`,
			newExpires, string(t.team), req.Key, req.HolderID,
		); err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		return runstate.AcquireSlotResponse{
			Kind:             runstate.AcquireGranted,
			HolderID:         req.HolderID,
			LeaseExpiresAt:   time.Unix(0, newExpires),
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
		}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return runstate.AcquireSlotResponse{}, err
	}

	acct, err := txConcurrencyAccounting(ctx, tx, t.team, req.Key, nowNS)
	if err != nil {
		return runstate.AcquireSlotResponse{}, err
	}
	activeCost := acct.used
	effCap := acct.effectiveCapacity(req.Capacity)

	policy := req.Policy

	queueBlocked := false
	if policy == onLimitQueue {
		blocked, err := txEarlierRunnableQueueWaiter(ctx, tx, t.team, req.Key, req.RunID, req.NodeID, nowNS, acct)
		if err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		queueBlocked = blocked
	}
	if !queueBlocked && fitsBudget(activeCost, req.Cost, effCap) {
		expiresNS := now.Add(req.Lease).UnixNano()
		if err := txInsertHolder(ctx, tx, holderRow{
			team: t.team, key: req.Key, holderID: req.HolderID, runID: req.RunID, nodeID: req.NodeID,
			cost: req.Cost, declaredCapacity: req.Capacity,
		}, nowNS, expiresNS); err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		return runstate.AcquireSlotResponse{
			Kind:             runstate.AcquireGranted,
			HolderID:         req.HolderID,
			LeaseExpiresAt:   time.Unix(0, expiresNS),
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
		}, nil
	}

	switch policy {
	case onLimitSkip:
		return runstate.AcquireSlotResponse{Kind: runstate.AcquireSkipped, PreviousCapacity: prevCap, DriftNote: driftNote}, nil

	case onLimitFail:
		return runstate.AcquireSlotResponse{Kind: runstate.AcquireFailed, PreviousCapacity: prevCap, DriftNote: driftNote}, nil

	case onLimitCancelOthers:
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
				return runstate.AcquireSlotResponse{}, err
			}
			if fence != nil {
				if err := cancelSupersededCapacityTx(ctx, tx, t.team, req.Key, id, fence.actor); err != nil {
					return runstate.AcquireSlotResponse{}, err
				}
			}
		}
		expiresNS := now.Add(req.Lease).UnixNano()
		if err := txInsertHolder(ctx, tx, holderRow{
			team: t.team, key: req.Key, holderID: req.HolderID, runID: req.RunID, nodeID: req.NodeID,
			cost: req.Cost, declaredCapacity: req.Capacity,
		}, nowNS, expiresNS); err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		return runstate.AcquireSlotResponse{
			Kind:             runstate.AcquireCancellingOthers,
			HolderID:         req.HolderID,
			LeaseExpiresAt:   time.Unix(0, expiresNS),
			SupersededIDs:    supersededIDs,
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
		}, nil

	case onLimitQueue:
		fallthrough
	default:
		arrivedNS := nowNS
		if queuedArrivedNS, queued, err := txQueueWaiterArrivedAt(ctx, tx, t.team, req.Key, req.RunID, req.NodeID); err != nil {
			return runstate.AcquireSlotResponse{}, err
		} else if queued {
			arrivedNS = queuedArrivedNS
		} else if arrivedNS, err = txNextQueueArrival(ctx, tx, t.team, req.Key, nowNS); err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		if err := txPark(ctx, tx, t.team, ConcurrencyWaiter{
			Key: req.Key, RunID: req.RunID, NodeID: req.NodeID, HolderID: req.HolderID,
			Policy: onLimitQueue,
			Cost:   req.Cost, DeclaredCapacity: req.Capacity,
		}, arrivedNS, now.Add(req.Lease).UnixNano()); err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		var position, queueLen int
		if err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE team = ? AND key = ? AND policy = ? AND arrived_at < ?`,
			string(t.team), req.Key, onLimitQueue, arrivedNS,
		).Scan(&position); err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		if err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE team = ? AND key = ? AND policy = ?`,
			string(t.team), req.Key, onLimitQueue,
		).Scan(&queueLen); err != nil {
			return runstate.AcquireSlotResponse{}, err
		}
		return runstate.AcquireSlotResponse{
			Kind:             runstate.AcquireQueued,
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
			Position:         position,
			QueueLength:      queueLen,
			Holders:          admissionHolders(acct.holders),
		}, nil
	}
}

func txActiveHolders(ctx context.Context, tx *storeTx, team Team, key string, nowNS int64) ([]ConcurrencyHolder, error) {
	rows, err := tx.queryContext(
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

	rows, err := tx.queryContext(
		ctx,
		`SELECT `+waiterColumns+`
		   FROM concurrency_waiters
		  WHERE team = ? AND key = ? AND policy = ? AND (run_id != ? OR node_id != ?) AND lease_expires_at > ?
		  ORDER BY arrived_at ASC`,
		string(team), key, onLimitQueue, runID, nodeID, nowNS,
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
		if !deadline.IsZero() && !deadline.After(tx.now(ctx)) {
			continue
		}
		if currentQueued && w.ArrivedAt.UnixNano() >= currentArrivedNS {
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
		string(team), key, runID, nodeID, onLimitQueue, tx.now(ctx).UnixNano(),
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
	rows, err := tx.queryContext(
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
		case onLimitQueue, onLimitCancelOthers:
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
	err := tx.QueryRowContext(ctx, `SELECT MAX(deadline) FROM (
 SELECT CASE WHEN `+controllerRunOwnership+` THEN CAST(? AS BIGINT) ELSE (SELECT lease_expires_at FROM triggers WHERE team=r.team AND id=r.id AND status='claimed') END AS deadline FROM runs r WHERE team=? AND id=?
 UNION ALL SELECT lease_expires_at FROM nodes WHERE team=? AND run_id=? AND status<>'done' AND claimed_by IS NOT NULL AND (?='' OR node_id=?)
) live`, tx.now(ctx).Add(controllerOwnedHorizon).UnixNano(), team, run, team, run, job, job).Scan(&lease)
	if err != nil {
		return 0, err
	}
	if !lease.Valid {
		return 0, holderRefusal()
	}
	drain, err := cancelDrainTx(ctx, tx, team, run, job)
	if err != nil {
		return 0, err
	}
	now := tx.now(ctx)
	bounds := []time.Time{time.Unix(0, expires), drain, time.Unix(0, lease.Int64)}
	if job != "" {
		generation, err := capacityGenerationTx(ctx, tx, team, run, job)
		if err != nil {
			return 0, err
		}
		funded, err := fundedBoundTx(ctx, tx, team, UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(run), NodeID: runspec.NodeID(job)}, generation, now)
		if err != nil {
			return 0, err
		}
		bounds = append(bounds, funded)
	}
	bound, err := leaseBound(now, bounds...)
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
	res, err := tx.execContext(
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

func txPark(ctx context.Context, tx *storeTx, team Team, w ConcurrencyWaiter, arrivedNS, expiresNS int64) error {
	generation, err := capacityGenerationTx(ctx, tx, team, w.RunID, w.NodeID)
	if err != nil {
		return err
	}
	expiresNS, err = capacityLeaseCeilingTx(ctx, tx, team, w.RunID, w.NodeID, expiresNS)
	if err != nil {
		return err
	}
	_, err = tx.execContext(
		ctx,
		`INSERT INTO concurrency_waiters
		   (team, key, run_id, node_id, holder_id, arrived_at, policy, cost, declared_capacity, generation, lease_expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (team, key, run_id, node_id) DO UPDATE SET
		   holder_id         = excluded.holder_id,
		   arrived_at        = excluded.arrived_at,
		   policy            = excluded.policy,
		   cost              = excluded.cost,
		   declared_capacity = excluded.declared_capacity,
		   generation        = excluded.generation,
		   lease_expires_at  = excluded.lease_expires_at`,
		string(team), w.Key, w.RunID, w.NodeID, w.HolderID, arrivedNS, w.Policy, w.Cost, w.DeclaredCapacity, generation, expiresNS,
	)
	return err
}

func txSupersede(ctx context.Context, tx *storeTx, team Team, key, holderID string) error {
	var run, node string
	if err := tx.QueryRowContext(ctx, `SELECT run_id,node_id FROM concurrency_holders WHERE team=? AND key=? AND holder_id=?`, team, key, holderID).Scan(&run, &node); err != nil {
		return err
	}
	var owned bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admission_work WHERE team=? AND run_id=? AND node_id=?)`, team, run, node).Scan(&owned); err != nil {
		return err
	}
	if owned {
		if _, err := tx.execContext(ctx, `UPDATE admission_work SET granted=0 WHERE team=? AND run_id=? AND node_id=?`, team, run, node); err != nil {
			return err
		}
		_, err := tx.execContext(ctx, `UPDATE concurrency_holders SET superseded=1 WHERE team=? AND run_id=? AND node_id=?`, team, run, node)
		return err
	}
	_, err := tx.execContext(ctx, `UPDATE concurrency_holders SET superseded=1 WHERE team=? AND key=? AND holder_id=?`, team, key, holderID)
	return err
}

func txDeleteWaiter(ctx context.Context, tx *storeTx, team Team, key, runID, nodeID string) (bool, error) {
	res, err := tx.execContext(
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

	nowNS := tx.now(ctx).UnixNano()
	hrows, err := tx.queryContext(
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

	wrows, err := tx.queryContext(
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
		w := &state.Waiters[i]
		if level, _, _, group, ok := runspec.ParseConcurrencyKey(key); ok && level == "plan" && w.NodeID == "" {
			released, err := planWorkReleasedTx(ctx, tx, t.team, w.RunID)
			if err != nil {
				return nil, err
			}
			if released {
				w.BlockingReason = "waiting to reacquire " + group
			}
		}
		if state.Waiters[i].Policy == onLimitQueue {
			state.Waiters[i].Position = qrank
			qrank++
		}
	}
	if floor > 0 {
		state.EffectiveCapacity = floor
	}
	return state, nil
}

func reapStaleConcurrencyHoldersTx(ctx context.Context, tx *storeTx, team Team, key string) ([]ConcurrencyHolder, error) {
	rows, err := tx.queryContext(
		ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders WHERE team=? AND key=?`+tx.forUpdate(), team, key,
	)
	if err != nil {
		return nil, err
	}
	var holders []ConcurrencyHolder
	for rows.Next() {
		h, err := scanHolder(rows)
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		holders = append(holders, h)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var stale []ConcurrencyHolder
	for _, h := range holders {
		live, err := capacityWorkLiveTx(ctx, tx, team, runstate.CapacityBinding{RunID: h.RunID, NodeID: h.NodeID}, h.LeaseExpiresAt.UnixNano())
		if err != nil {
			return nil, err
		}
		if live {
			continue
		}
		stale = append(stale, h)
		if err := discardAdmissionLevelTx(ctx, tx, h.team, h.RunID, h.NodeID); err != nil {
			return nil, err
		}
	}
	return stale, nil
}

func reapStaleConcurrencyWaitersTx(ctx context.Context, tx *storeTx, team Team, key string) ([]ConcurrencyWaiter, error) {
	rows, err := tx.queryContext(ctx, `SELECT `+waiterColumns+` FROM concurrency_waiters WHERE team=? AND key=?`, team, key)
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
	var stale []ConcurrencyWaiter
	for _, w := range dropped {
		live, err := capacityWaiterCurrentTx(ctx, tx, team, w)
		if err != nil {
			return nil, err
		}
		if live {
			continue
		}
		stale = append(stale, w)
	}
	return stale, nil
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
	var arrivedNS int64
	if err := rs.Scan(&w.Key, &w.RunID, &w.NodeID, &w.HolderID, &arrivedNS, &w.Policy,
		&w.Cost, &w.DeclaredCapacity, &w.team, &w.generation); err != nil {
		return ConcurrencyWaiter{}, err
	}
	w.ArrivedAt = time.Unix(0, arrivedNS)
	return w, nil
}

const waiterColumns = `key, run_id, node_id, holder_id, arrived_at, policy, cost, declared_capacity, team, generation`

func nodeIDOrDash(nodeID string) string {
	if strings.TrimSpace(nodeID) == "" {
		return "-"
	}
	return nodeID
}

func admissionHolders(rows []ConcurrencyHolder) []runstate.ConcurrencyHolder {
	var holders []runstate.ConcurrencyHolder
	for _, h := range rows {
		holders = append(holders, h.ConcurrencyHolder)
	}
	return holders
}
