package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
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

// holderLiveSQL is the canonical SQL predicate for a holder row that
// still counts toward its key's budget: not superseded and lease
// unexpired. It consumes exactly one bind parameter, the current time
// in nanoseconds. alias prefixes the column names (pass "h." for an
// aliased join, "" otherwise). Every query that filters holders by
// liveness must use this fragment; holderCountsForBudget is its Go-side
// twin for rows already scanned.
func holderLiveSQL(alias string) string {
	return alias + "superseded = 0 AND " + alias + "lease_expires_at > ?"
}

func holderLeaseLiveSQL(alias string) string {
	return alias + "lease_expires_at > ?"
}

// holderCountsForBudget reports whether an already-scanned holder row
// still counts toward its key's budget. It is the Go-side twin of
// holderLiveSQL; the two must answer identically. The heartbeat path
// deliberately does NOT use it: a superseded holder with a live lease
// still heartbeats successfully (that is how it learns it was
// superseded), so heartbeat checks the lease alone.
func holderCountsForBudget(superseded bool, leaseExpiresNS, nowNS int64) bool {
	return !superseded && leaseExpiresNS > nowNS
}

// declaredCapacityFloorTerm is one live holder's contribution to the
// capacity floor: its own declaration, or the most-restrictive capacity
// (1) when the row predates declared-capacity tracking (zero or
// negative), so a legacy holder constrains admission instead of
// vanishing from the floor and inflating it into over-admission.
func declaredCapacityFloorTerm(declared int) int {
	if declared > 0 {
		return declared
	}
	return 1
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

// DefaultConcurrencyLease is DefaultLeaseDuration by construction so
// holder and claim leases can't drift apart; a holder must be silent
// for the full window to be reaped.
const DefaultConcurrencyLease = DefaultLeaseDuration

// DefaultConcurrencyHeartbeatInterval is the fast holder-heartbeat cadence
// for CancelOthers slots, where a superseding acquirer must be noticed
// within ~3s. Other policies refresh at the slower cadence returned by
// ConcurrencyHeartbeatInterval.
const DefaultConcurrencyHeartbeatInterval = 3 * time.Second

// DefaultConcurrencyHeartbeatTimeout bounds one heartbeat attempt on the
// fast CancelOthers cadence; it is shorter than that 3s interval so a
// wedged tick can't stack. The policy-correct bound is
// ConcurrencyHeartbeatTimeout.
const DefaultConcurrencyHeartbeatTimeout = 2 * time.Second

// ConcurrencyHeartbeatInterval is how often a holder under onLimit refreshes
// its lease. Only CancelOthers needs the fast 3s cadence, to notice a
// superseding acquirer within one tick; other policies have no supersede to
// observe and refresh at lease/3. With one holder per memo node plus the
// plan slot sharing one store, the slower cadence is a fraction of the
// write load.
func ConcurrencyHeartbeatInterval(onLimit string) time.Duration {
	if onLimit == OnLimitCancelOthers {
		return DefaultConcurrencyHeartbeatInterval
	}
	return DefaultConcurrencyLease / 3
}

// ConcurrencyHeartbeatTimeout bounds one heartbeat attempt under onLimit. An
// _txlock=immediate heartbeat can busy-wait seconds for the write lock, so
// the slow cadence needs a bound long enough to outwait that: a short bound
// abandons a winnable attempt and counts a false miss, and at lease/3 only
// three misses lapse a live holder's lease. The bound stays below the slow
// interval so attempts can't stack; CancelOthers keeps the short
// DefaultConcurrencyHeartbeatTimeout against its 3s cadence.
func ConcurrencyHeartbeatTimeout(onLimit string) time.Duration {
	if onLimit == OnLimitCancelOthers {
		return DefaultConcurrencyHeartbeatTimeout
	}
	return DefaultConcurrencyLease / 4
}

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

const inheritedHolderDeclaredCapacity = -1
const inheritedHolderNodePrefix = "\x00inherited:"

func inheritedHolderNodeID(holderID string) string {
	return inheritedHolderNodePrefix + holderID
}

func isInheritedHolderNodeID(nodeID string) bool {
	return strings.HasPrefix(nodeID, inheritedHolderNodePrefix)
}

func holderBudgetCost(cost, declaredCapacity int) int {
	if cost == 0 && declaredCapacity == inheritedHolderDeclaredCapacity {
		return 0
	}
	if cost <= 0 {
		return 1
	}
	return cost
}

func holderFloorTerm(cost, declaredCapacity int) (int, bool) {
	if cost == 0 && declaredCapacity == inheritedHolderDeclaredCapacity {
		return 0, false
	}
	return declaredCapacityFloorTerm(declaredCapacity), true
}

// AcquireSlotRequest: empty CacheKeyHash = no memo; empty NodeID =
// plan-level. HolderID convention: "runID/nodeID" or "runID/-".
//
// BypassRead suppresses the cache-lookup branch: the request flows
// straight into the capacity/coalesce/queue path as if no prior entry
// existed for this key. Cache WRITES at release time are unaffected.
// Used by --no-cache so a run forces fresh execution but still
// populates the runs store for subsequent runs over the same content.
type AcquireSlotRequest struct {
	Key      string
	HolderID string
	// InheritedHolderID joins and refreshes an existing live holder
	// instead of creating HolderID. Its Cost is already accounted.
	InheritedHolderID string
	RunID             string
	NodeID            string
	Capacity          int
	Policy            string
	CacheKeyHash      string
	CacheTTL          time.Duration
	CancelTimeout     time.Duration
	Lease             time.Duration
	BypassRead        bool

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
	QueueArrivedAt time.Time
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

// ActiveConcurrencyHolder returns one non-superseded holder with an
// unexpired lease. ErrNotFound means the key/holder pair is not
// currently admitted.
func (s *Store) ActiveConcurrencyHolder(ctx context.Context, key, holderID string, now time.Time) (*ConcurrencyHolder, error) {
	return s.concurrencyHolder(ctx, key, holderID, now, holderLiveSQL(""))
}

// ConcurrencyHolder returns one unexpired holder row, including
// superseded holders. ErrNotFound means the key/holder pair is absent
// or its lease has expired.
func (s *Store) ConcurrencyHolder(ctx context.Context, key, holderID string, now time.Time) (*ConcurrencyHolder, error) {
	return s.concurrencyHolder(ctx, key, holderID, now, holderLeaseLiveSQL(""))
}

func (s *Store) concurrencyHolder(ctx context.Context, key, holderID string, now time.Time, livePredicate string) (*ConcurrencyHolder, error) {
	row := s.queryRow(ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders
		  WHERE key = ?
		    AND holder_id = ?
		    AND `+livePredicate,
		key, holderID, now.UnixNano(),
	)
	holder, err := scanHolder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	scrubInheritedHolder(&holder)
	return &holder, nil
}

func scrubInheritedHolder(holder *ConcurrencyHolder) {
	if holder.DeclaredCapacity == inheritedHolderDeclaredCapacity {
		holder.DeclaredCapacity = 0
	}
	if isInheritedHolderNodeID(holder.NodeID) {
		holder.NodeID = ""
	}
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

	if req.InheritedHolderID != "" && req.CacheKeyHash != "" {
		return AcquireSlotResponse{}, errors.New("concurrency: inherited holder cannot be used with cache acquisition")
	}
	if req.InheritedHolderID == "" && req.Cost > req.Capacity {
		if req.Policy == OnLimitSkip {
			return AcquireSlotResponse{Kind: AcquireSkipped}, nil
		}
		return AcquireSlotResponse{Kind: AcquireFailed}, nil
	}

	tx, err := s.beginTx(ctx)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: entries-row lock before any other row touch -- one lock
	// order (entries, then cache, then holders/waiters) across every
	// path, so a release writing cache after deleting its holder can't
	// deadlock an acquire that read cache first.
	if err := txLockEntry(ctx, tx, s.forUpdate(), req.Key); err != nil {
		return AcquireSlotResponse{}, err
	}

	// safety: read the clock only after the tx holds the write lock; a
	// pre-BEGIN timestamp goes stale while waiting and revives expired
	// holders whose budget was already reassigned.
	now := time.Now()
	nowNS := now.UnixNano()

	if reaped, err := txReapTerminalConcurrencyHolders(ctx, tx, req.Key); err != nil {
		return AcquireSlotResponse{}, err
	} else if len(reaped) > 0 {
		if _, err := txPromoteWaitersLocked(ctx, tx, req.Key, nowNS, now.Add(req.Lease).UnixNano(), livePollingWaiter{}); err != nil {
			return AcquireSlotResponse{}, err
		}
	}

	hit, err := txCacheLookup(ctx, tx, req.Key, req.CacheKeyHash, nowNS, req.BypassRead, true)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	if hit != nil {
		if err := txCommitChecked(ctx, tx, nowNS, req.Key); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{
			Kind:         AcquireCached,
			OutputRef:    hit.OutputRef,
			OriginRunID:  hit.OriginRunID,
			OriginNodeID: hit.OriginNodeID,
		}, nil
	}

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
		// safety: a conflict-losing insert holds no row lock; re-read FOR
		// UPDATE so two first-acquires of a key serialize like later pairs.
		var relockCap int
		if err := tx.QueryRowContext(
			ctx,
			`SELECT capacity FROM concurrency_entries WHERE key = ?`+s.forUpdate(), req.Key,
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

	if req.InheritedHolderID != "" {
		expires, err := txRefreshInheritedHolder(ctx, tx, req.Key, req.InheritedHolderID, now, req.Lease)
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := txInsertHolder(ctx, tx, holderRow{
			key: req.Key, holderID: req.HolderID, runID: req.RunID, nodeID: inheritedHolderNodeID(req.InheritedHolderID),
			cost: 0, declaredCapacity: inheritedHolderDeclaredCapacity,
		}, nowNS, expires.UnixNano()); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := txCommitChecked(ctx, tx, nowNS, req.Key); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{
			Kind:             AcquireGranted,
			HolderID:         req.HolderID,
			LeaseExpiresAt:   expires,
			PreviousCapacity: prevCap,
			DriftNote:        driftNote,
		}, nil
	}

	var existingLeaseNS int64
	var existingSuperInt int
	err = tx.QueryRowContext(
		ctx,
		`SELECT lease_expires_at, superseded FROM concurrency_holders
		  WHERE key = ? AND holder_id = ?`,
		req.Key, req.HolderID,
	).Scan(&existingLeaseNS, &existingSuperInt)
	if err == nil && holderCountsForBudget(existingSuperInt == 1, existingLeaseNS, nowNS) {
		newExpires := now.Add(req.Lease).UnixNano()
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE concurrency_holders SET lease_expires_at = ? WHERE key = ? AND holder_id = ?`,
			newExpires, req.Key, req.HolderID,
		); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := txCommitChecked(ctx, tx, nowNS, req.Key); err != nil {
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

	acct, err := txConcurrencyAccounting(ctx, tx, req.Key, nowNS)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	activeCost := acct.used
	effCap := acct.effectiveCapacity(req.Capacity)

	policy := req.Policy
	if policy == OnLimitCoalesce && req.BypassRead {
		policy = OnLimitQueue
	}

	fifoBlocked := false
	if policy == OnLimitQueue {
		var earlier int
		if err := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters
			  WHERE key = ? AND policy = ? AND (run_id != ? OR node_id != ?)`,
			req.Key, OnLimitQueue, req.RunID, req.NodeID,
		).Scan(&earlier); err != nil {
			return AcquireSlotResponse{}, err
		}
		fifoBlocked = earlier > 0
	}
	if !fifoBlocked && fitsBudget(activeCost, req.Cost, effCap) {
		expiresNS := now.Add(req.Lease).UnixNano()
		if err := txInsertHolder(ctx, tx, holderRow{
			key: req.Key, holderID: req.HolderID, runID: req.RunID, nodeID: req.NodeID,
			cost: req.Cost, declaredCapacity: req.Capacity,
		}, nowNS, expiresNS); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := txCommitChecked(ctx, tx, nowNS, req.Key); err != nil {
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
		if err := txCommitChecked(ctx, tx, nowNS, req.Key); err != nil {
			return AcquireSlotResponse{}, err
		}
		return AcquireSlotResponse{Kind: AcquireSkipped, PreviousCapacity: prevCap, DriftNote: driftNote}, nil

	case OnLimitFail:
		if err := txCommitChecked(ctx, tx, nowNS, req.Key); err != nil {
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
		if err := txPark(ctx, tx, ConcurrencyWaiter{
			Key: req.Key, RunID: req.RunID, NodeID: req.NodeID, HolderID: req.HolderID,
			Policy: OnLimitCoalesce, CacheKeyHash: req.CacheKeyHash,
			LeaderRunID: leaderRun, LeaderNodeID: leaderNode,
			Cost: req.Cost, DeclaredCapacity: req.Capacity,
		}, nowNS); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := txCommitChecked(ctx, tx, nowNS, req.Key); err != nil {
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
		var supersededIDs []string
		freed := 0
		for _, h := range acct.holders {
			if fitsBudget(activeCost-freed, req.Cost, effCap) && len(supersededIDs) > 0 {
				break
			}
			supersededIDs = append(supersededIDs, h.HolderID)
			freed += holderBudgetCost(h.Cost, h.DeclaredCapacity)
		}
		var expandedSupersededIDs []string
		for _, hid := range supersededIDs {
			ids, err := txSupersedeHolderAndInherited(ctx, tx, req.Key, hid, nowNS)
			if err != nil {
				return AcquireSlotResponse{}, err
			}
			expandedSupersededIDs = append(expandedSupersededIDs, ids...)
		}
		supersededIDs = expandedSupersededIDs
		expiresNS := now.Add(req.Lease).UnixNano()
		if err := txInsertHolder(ctx, tx, holderRow{
			key: req.Key, holderID: req.HolderID, runID: req.RunID, nodeID: req.NodeID,
			cost: req.Cost, declaredCapacity: req.Capacity,
		}, nowNS, expiresNS); err != nil {
			return AcquireSlotResponse{}, err
		}
		if err := txCommitChecked(ctx, tx, nowNS, req.Key); err != nil {
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
		if err := txPark(ctx, tx, ConcurrencyWaiter{
			Key: req.Key, RunID: req.RunID, NodeID: req.NodeID, HolderID: req.HolderID,
			Policy: OnLimitQueue, CacheKeyHash: req.CacheKeyHash,
			Cost: req.Cost, DeclaredCapacity: req.Capacity,
		}, nowNS); err != nil {
			return AcquireSlotResponse{}, err
		}
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
		if err := txCommitChecked(ctx, tx, nowNS, req.Key); err != nil {
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

// txActiveHolders reads the current live (per holderLiveSQL) holders
// for a key within an open transaction, oldest claim first.
func txActiveHolders(ctx context.Context, tx *storeTx, key string, nowNS int64) ([]ConcurrencyHolder, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders
		  WHERE key = ? AND `+holderLiveSQL("")+`
		  ORDER BY claimed_at ASC`,
		key, nowNS,
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

// concurrencyAccounting is one consistent view of a key's admission
// state: the used cost, the capacity floor, and the live holder set are
// all derived from a single scan inside one transaction, so they cannot
// disagree with each other by construction. Every admission, promotion,
// and eviction decision routes through it.
type concurrencyAccounting struct {
	// used is the summed admission cost of the live holders.
	used int
	// floor is the most-restrictive declared capacity over the live
	// holders (via declaredCapacityFloorTerm); 0 means no live holder
	// constrains the budget. Parked waiters are NOT folded in -- a
	// non-admitted waiter holds no budget, so letting its declaration
	// drag the effective capacity below the already-admitted holders
	// would invert priority and starve a FIFO head that fits under its
	// own capacity.
	floor int
	// entryCap is the registered capacity from the entries row,
	// defaulted to 1 when missing or non-positive.
	entryCap int
	// holders are the live holders, oldest claim first.
	holders []ConcurrencyHolder
}

// effectiveCapacity resolves the most-restrictive-wins capacity for an
// arrival declaring the given capacity. A non-positive incoming is
// ignored (the release path has no arrival; the promote path folds each
// candidate's own capacity itself). When neither the live holders nor
// the arrival constrain the budget, the entries row is the fallback.
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

// txConcurrencyAccounting computes the key's admission state from one
// scan of the live holders plus the entries row.
func txConcurrencyAccounting(ctx context.Context, tx *storeTx, key string, nowNS int64) (concurrencyAccounting, error) {
	entryCap, err := txEntryCapacity(ctx, tx, key)
	if err != nil {
		return concurrencyAccounting{}, err
	}
	holders, err := txActiveHolders(ctx, tx, key, nowNS)
	if err != nil {
		return concurrencyAccounting{}, err
	}
	a := concurrencyAccounting{entryCap: entryCap, holders: holders}
	for _, h := range holders {
		a.used += holderBudgetCost(h.Cost, h.DeclaredCapacity)
		if t, ok := holderFloorTerm(h.Cost, h.DeclaredCapacity); ok && (a.floor == 0 || t < a.floor) {
			a.floor = t
		}
	}
	return a, nil
}

// txLockEntry serializes the transaction on the key's entries row --
// the same lock the acquire path takes -- so every budget-mutating
// path (admission, promotion, lease extension) runs under per-key
// mutual exclusion on Postgres. Callers pass Store.forUpdate(); SQLite
// passes an empty suffix because its writers serialize at the database
// level. A key with no entries row has no holders or waiters to race
// over, so a missing row is not an error.
func txLockEntry(ctx context.Context, tx *storeTx, forUpdate, key string) error {
	var one int
	err := tx.QueryRowContext(
		ctx,
		`SELECT 1 FROM concurrency_entries WHERE key = ?`+forUpdate, key,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
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
	if _, err := txReapTerminalConcurrencyHolders(ctx, tx, key); err != nil {
		return nil, err
	}
	return txPromoteWaitersLocked(ctx, tx, key, nowNS, expiresNS, livePollingWaiter{})
}

func txPromotePollingWaiter(ctx context.Context, tx *storeTx, key, runID, nodeID string, nowNS, expiresNS int64) ([]ConcurrencyWaiter, error) {
	if _, err := txReapTerminalConcurrencyHolders(ctx, tx, key); err != nil {
		return nil, err
	}
	return txPromoteWaitersLocked(ctx, tx, key, nowNS, expiresNS, livePollingWaiter{runID: runID, nodeID: nodeID})
}

type livePollingWaiter struct {
	runID  string
	nodeID string
}

func (p livePollingWaiter) matches(waiter ConcurrencyWaiter) bool {
	return p.runID != "" && waiter.RunID == p.runID && waiter.NodeID == p.nodeID
}

func txPromoteWaitersLocked(ctx context.Context, tx *storeTx, key string, nowNS, expiresNS int64, polling livePollingWaiter) ([]ConcurrencyWaiter, error) {
	acct, err := txConcurrencyAccounting(ctx, tx, key, nowNS)
	if err != nil {
		return nil, err
	}
	entryCap, holderMin, used := acct.entryCap, acct.floor, acct.used

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

	ids := make([]string, 0, len(candidates))
	for _, w := range candidates {
		ids = append(ids, w.RunID)
	}
	live, err := txLiveRunningRunIDs(ctx, tx, ids, time.Unix(0, nowNS).Add(-DefaultLeaseDuration).UnixNano())
	if err != nil {
		return nil, err
	}

	var promoted []ConcurrencyWaiter
	for _, w := range candidates {
		if !live[w.RunID] && !polling.matches(w) {
			if _, derr := txDeleteWaiter(ctx, tx, w.Key, w.RunID, w.NodeID); derr != nil {
				return nil, derr
			}
			continue
		}
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
			break
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
		if _, err := txDeleteWaiter(ctx, tx, w.Key, w.RunID, w.NodeID); err != nil {
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
			key: w.Key, holderID: newHolder, runID: w.RunID, nodeID: w.NodeID,
			cost: c, declaredCapacity: dc, queueArrivedNS: w.ArrivedAt.UnixNano(),
		}, nowNS, expiresNS); err != nil {
			return nil, err
		}
		promoted[i].HolderID = newHolder
	}
	return promoted, nil
}

func txLiveRunningRunIDs(ctx context.Context, tx *storeTx, ids []string, heartbeatCutoff int64) (map[string]bool, error) {
	seen := make(map[string]bool, len(ids))
	args := make([]any, 0, len(ids)+2)
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
	args = append(args, runStatusRunning, heartbeatCutoff)
	rows, err := tx.QueryContext(
		ctx,
		`SELECT id FROM runs
		  WHERE id IN (`+strings.Join(placeholders, ",")+`)
		    AND status = ?
		    AND last_heartbeat_at >= ?`,
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

// concurrencyInvariantFailFast selects how a violated concurrency
// invariant reports. Under `go test` (testing.Testing()) a violation
// fails the transaction, so every suite in the repo doubles as an
// invariant monitor. In production it logs loudly and commits anyway:
// a violation can also be produced by rows written by older binaries
// (holders that predate declared-capacity tracking), and refusing to
// commit would wedge release and promote paths on data this code
// didn't write.
var concurrencyInvariantFailFast = testing.Testing()

// txCommitChecked verifies the concurrency invariants for every key a
// mutating transaction touched, then commits. Every mutating
// concurrency transaction must commit through it, so a path that
// violates an invariant is caught at its own boundary no matter which
// site drifted.
func txCommitChecked(ctx context.Context, tx *storeTx, nowNS int64, keys ...string) error {
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		if err := txCheckConcurrencyInvariants(ctx, tx, k, nowNS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// txCheckConcurrencyInvariants asserts the cross-site invariants for
// one key: live cost never exceeds the effective capacity, no live
// holder outweighs its own declaration, every waiter carries a known
// policy with the leader shape that policy requires, and no
// participant both holds and waits on the same key.
func txCheckConcurrencyInvariants(ctx context.Context, tx *storeTx, key string, nowNS int64) error {
	acct, err := txConcurrencyAccounting(ctx, tx, key, nowNS)
	if err != nil {
		return err
	}
	var violations []string
	if eff := acct.effectiveCapacity(0); acct.used > eff {
		violations = append(violations, fmt.Sprintf("live cost %d exceeds effective capacity %d", acct.used, eff))
	}
	for _, h := range acct.holders {
		if h.DeclaredCapacity > 0 && holderBudgetCost(h.Cost, h.DeclaredCapacity) > h.DeclaredCapacity {
			violations = append(violations, fmt.Sprintf("holder %q cost %d exceeds its declared capacity %d", h.HolderID, h.Cost, h.DeclaredCapacity))
		}
	}
	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+waiterColumns+` FROM concurrency_waiters WHERE key = ?`, key,
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
		case OnLimitCoalesce:
			if w.LeaderRunID == "" {
				violations = append(violations, fmt.Sprintf("coalesce waiter %s/%s has no leader", w.RunID, w.NodeID))
			}
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
	msg := fmt.Sprintf("concurrency invariants violated for key %q: %s", key, strings.Join(violations, "; "))
	if concurrencyInvariantFailFast {
		return errors.New(msg)
	}
	slog.Error(msg)
	return nil
}

// holderRow is the input to txInsertHolder: the identity, weight, and
// declaration a new admission stamps onto its holder row.
type holderRow struct {
	key              string
	holderID         string
	runID            string
	nodeID           string
	cost             int
	declaredCapacity int
	queueArrivedNS   int64
}

// txInsertHolder mints the live holder row for an admission -- the
// single site that writes into concurrency_holders. A conflicting row
// is reclaimed in place only when it no longer counts toward the budget
// (superseded by a CancelOthers eviction, or lease-expired); both arise
// from a same-holder_id re-acquire or promotion after a crash,
// redelivery, or an eviction the reaper hasn't swept. A conflicting
// LIVE row is never clobbered: the insert fails loudly instead, so a
// path that forgot to check liveness before admitting surfaces as an
// error rather than as a silently stolen slot.
//
// Minting also deletes any waiter row this participant left parked: an
// admitted arrival is by definition no longer waiting, and a stale row
// would later be promoted on top of its own live holder, aborting an
// unrelated release.
func txInsertHolder(ctx context.Context, tx *storeTx, h holderRow, nowNS, expiresNS int64) error {
	if _, err := txDeleteWaiter(ctx, tx, h.key, h.runID, h.nodeID); err != nil {
		return err
	}
	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO concurrency_holders
		   (key, holder_id, run_id, node_id, claimed_at, queue_arrived_at, lease_expires_at, superseded, cost, declared_capacity)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
		 ON CONFLICT (key, holder_id) DO UPDATE SET
		   run_id            = excluded.run_id,
		   node_id           = excluded.node_id,
		   claimed_at        = excluded.claimed_at,
		   queue_arrived_at  = excluded.queue_arrived_at,
		   lease_expires_at  = excluded.lease_expires_at,
		   superseded        = 0,
		   cost              = excluded.cost,
		   declared_capacity = excluded.declared_capacity
		 WHERE concurrency_holders.superseded = 1 OR concurrency_holders.lease_expires_at <= ?`,
		h.key, h.holderID, h.runID, h.nodeID, nowNS, h.queueArrivedNS, expiresNS, h.cost, h.declaredCapacity, nowNS,
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

func txRefreshInheritedHolder(
	ctx context.Context,
	tx *storeTx,
	key string,
	holderID string,
	now time.Time,
	lease time.Duration,
) (time.Time, error) {
	var superInt int
	var leaseNS int64
	err := tx.QueryRowContext(
		ctx,
		`SELECT superseded, lease_expires_at FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	).Scan(&superInt, &leaseNS)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, fmt.Errorf("concurrency: inherited holder %q for key %q is not active", holderID, key)
	}
	if err != nil {
		return time.Time{}, err
	}
	if superInt == 1 {
		return time.Time{}, fmt.Errorf("%w: inherited holder %q for key %q", ErrConcurrencySuperseded, holderID, key)
	}
	if leaseNS <= now.UnixNano() {
		return time.Time{}, fmt.Errorf("concurrency: inherited holder %q for key %q is expired", holderID, key)
	}
	expires := now.Add(lease)
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE concurrency_holders SET lease_expires_at = ? WHERE key = ? AND holder_id = ?`,
		expires.UnixNano(), key, holderID,
	); err != nil {
		return time.Time{}, err
	}
	return expires, nil
}

func txSupersedeHolderAndInherited(ctx context.Context, tx *storeTx, key, holderID string, nowNS int64) ([]string, error) {
	var nodeID string
	err := tx.QueryRowContext(
		ctx,
		`SELECT node_id FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	).Scan(&nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := txSupersede(ctx, tx, key, holderID); err != nil {
		return nil, err
	}
	ids := []string{holderID}
	inheritedMarker := inheritedHolderNodeID(holderID)
	originalMarker := inheritedMarker
	if isInheritedHolderNodeID(nodeID) {
		originalMarker = nodeID
	}
	rows, err := tx.QueryContext(
		ctx,
		`SELECT holder_id FROM concurrency_holders
		  WHERE key = ? AND `+holderLiveSQL("")+` AND node_id IN (?, ?)
		  ORDER BY claimed_at ASC`,
		key, nowNS, inheritedMarker, originalMarker,
	)
	if err != nil {
		return nil, err
	}
	var children []string
	for rows.Next() {
		var childID string
		if err := rows.Scan(&childID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		children = append(children, childID)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, childID := range children {
		childIDs, err := txSupersedeHolderAndInherited(ctx, tx, key, childID, nowNS)
		if err != nil {
			return nil, err
		}
		ids = append(ids, childIDs...)
	}
	return ids, nil
}

// HeartbeatConcurrencySlot extends the lease and reports the
// supersede signal; ErrLockHeld when caller no longer holds.
//
// The write is retried on a transient SQLITE_BUSY so a brief writer
// stall under contention does not lapse a live holder's lease. Each
// retry re-reads the clock and the lease-expiry check, so a retry can
// never revive a lease that lapsed while waiting; the bounded retry
// budget is a small fraction of the lease window.
func (s *Store) HeartbeatConcurrencySlot(ctx context.Context, key, holderID string, lease time.Duration) (expires time.Time, superseded bool, err error) {
	if lease <= 0 {
		lease = DefaultConcurrencyLease
	}
	err = retryOnBusy(func() error {
		var once error
		expires, superseded, once = s.heartbeatConcurrencySlotOnce(ctx, key, holderID, lease)
		return once
	})
	return expires, superseded, err
}

func (s *Store) heartbeatConcurrencySlotOnce(ctx context.Context, key, holderID string, lease time.Duration) (expires time.Time, superseded bool, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: entries-row lock first (same order as acquire) so a lease
	// extension can't race a promotion admitting into the same budget.
	if err := txLockEntry(ctx, tx, s.forUpdate(), key); err != nil {
		return time.Time{}, false, err
	}

	// safety: clock read after the lock, or a stale timestamp lets a
	// heartbeat revive a holder whose lease already lapsed.
	now := time.Now()
	expires = now.Add(lease)

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
	// safety: expired lease means admission may have reassigned the budget; reviving would double-admit.
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
	if err := txCommitChecked(ctx, tx, now.UnixNano(), key); err != nil {
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

	// safety: entries-row lock first, keeping the one lock order shared
	// with acquire and release+promote.
	if err := txLockEntry(ctx, tx, s.forUpdate(), key); err != nil {
		return false, err
	}

	released, _, _, err := txReleaseHolder(ctx, tx, key, holderID, outcome, outputRef, cacheKeyHash, ttl)
	if err != nil {
		return false, err
	}
	if err := txCommitChecked(ctx, tx, time.Now().UnixNano(), key); err != nil {
		return false, err
	}
	return released, nil
}

// txReleaseHolder is the single definition of "release this holder": it
// deletes the holder row and, when the release is a successful run of
// memoized content (outcome "success", a content hash, and a positive
// TTL), writes the shared cache entry. Reports whether a holder row
// matched, plus the released holder's (runID, nodeID).
func txReleaseHolder(ctx context.Context, tx *storeTx, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) (released bool, runID, nodeID string, err error) {
	var supersededInt int
	var leaseNS, queueArrivedNS int64
	var cost int
	var declaredCapacity int
	err = tx.QueryRowContext(
		ctx,
		`SELECT run_id, node_id, superseded, lease_expires_at, queue_arrived_at, cost, declared_capacity
		   FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	).Scan(&runID, &nodeID, &supersededInt, &leaseNS, &queueArrivedNS, &cost, &declaredCapacity)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", "", nil
	}
	if err != nil {
		return false, "", "", err
	}

	now := time.Now()
	if cost > 0 && supersededInt == 0 && leaseNS > now.UnixNano() {
		if err := txTransferInheritedHolderCost(ctx, tx, key, holderID, nodeID, cost, declaredCapacity, now.UnixNano()); err != nil {
			return false, "", "", err
		}
	}

	if nodeID == "" && queueArrivedNS > 0 {
		if err := txSupersede(ctx, tx, key, holderID); err != nil {
			return false, "", "", err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE concurrency_holders
   SET lease_expires_at = ?, cost = 0
 WHERE key = ? AND holder_id = ?`, now.UnixNano(), key, holderID); err != nil {
			return false, "", "", err
		}
	} else {
		if err := txDeleteHolder(ctx, tx, key, holderID); err != nil {
			return false, "", "", err
		}
	}

	if outcome == "success" && cacheKeyHash != "" && ttl > 0 {
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
			return false, "", "", err
		}
	}
	return true, runID, nodeID, nil
}

func txTransferInheritedHolderCost(
	ctx context.Context,
	tx *storeTx,
	key string,
	releasedHolderID string,
	releasedNodeID string,
	cost int,
	declaredCapacity int,
	nowNS int64,
) error {
	var inheritedHolderID string
	inheritedMarker := inheritedHolderNodeID(releasedHolderID)
	if releasedNodeID == "" {
		releasedNodeID = inheritedMarker
	}
	err := tx.QueryRowContext(
		ctx,
		`SELECT holder_id
		   FROM concurrency_holders
		  WHERE key = ?
		    AND holder_id != ?
		    AND superseded = 0
		    AND `+holderLeaseLiveSQL("")+`
		    AND cost = 0
		    AND declared_capacity = ?
		    AND node_id IN (?, ?)
		  ORDER BY claimed_at ASC
		  LIMIT 1`,
		key, releasedHolderID, nowNS, inheritedHolderDeclaredCapacity, inheritedMarker, releasedNodeID,
	).Scan(&inheritedHolderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE concurrency_holders
		    SET cost = ?, declared_capacity = ?
		  WHERE key = ? AND holder_id = ?`,
		cost, declaredCapacity, key, inheritedHolderID,
	)
	return err
}

// concurrencyCacheHit is a served memo entry.
type concurrencyCacheHit struct {
	OutputRef    string
	OriginRunID  string
	OriginNodeID string
}

// txCacheLookup is the single "serve this arrival from the memo
// cache?" decision, shared by the acquire path and the waiter-resolve
// path. A request with no content hash, or one that asked to bypass
// the read (--no-cache), never hits; an unexpired entry hits and
// touches last_hit_at. deleteExpired additionally drops an expired
// entry so the acquire path stops re-reading it; the polling resolve
// path leaves expiry to the sweeper.
func txCacheLookup(ctx context.Context, tx *storeTx, key, cacheKeyHash string, nowNS int64, bypassRead, deleteExpired bool) (*concurrencyCacheHit, error) {
	if cacheKeyHash == "" || bypassRead {
		return nil, nil
	}
	var hit concurrencyCacheHit
	var expiresNS int64
	err := tx.QueryRowContext(
		ctx,
		`SELECT output_ref, origin_run_id, origin_node_id, expires_at
		   FROM concurrency_cache
		  WHERE key = ? AND cache_key_hash = ?`,
		key, cacheKeyHash,
	).Scan(&hit.OutputRef, &hit.OriginRunID, &hit.OriginNodeID, &expiresNS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if expiresNS <= nowNS {
		if deleteExpired {
			if _, err := tx.ExecContext(
				ctx,
				`DELETE FROM concurrency_cache WHERE key = ? AND cache_key_hash = ? AND expires_at <= ?`,
				key, cacheKeyHash, nowNS,
			); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE concurrency_cache SET last_hit_at = ? WHERE key = ? AND cache_key_hash = ?`,
		nowNS, key, cacheKeyHash,
	); err != nil {
		return nil, err
	}
	return &hit, nil
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

	// safety: entries-row lock before any row writes (same order as
	// acquire) so release+promote serializes with admissions without
	// inverting lock order against a concurrent acquire.
	var hasEntry int
	err = tx.QueryRowContext(
		ctx,
		`SELECT 1 FROM concurrency_entries WHERE key = ?`+s.forUpdate(), key,
	).Scan(&hasEntry)
	entryDeclared := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil, err
	}

	released, runID, nodeID, err := txReleaseHolder(ctx, tx, key, holderID, outcome, outputRef, cacheKeyHash, ttl)
	if err != nil {
		return false, nil, nil, err
	}
	if released {
		followers, err = txDrainCoalesceFollowers(ctx, tx, key, runID, nodeID)
		if err != nil {
			return false, nil, nil, err
		}
	}

	if !entryDeclared {
		return released, followers, nil, txCommitChecked(ctx, tx, time.Now().UnixNano(), key)
	}
	now := time.Now()
	nowNS := now.UnixNano()
	expiresNS := now.Add(promoteLease).UnixNano()
	promoted, err = txPromoteWaiters(ctx, tx, key, nowNS, expiresNS)
	if err != nil {
		return false, nil, nil, err
	}

	if err := txCommitChecked(ctx, tx, nowNS, key); err != nil {
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

	out, err := txDrainCoalesceFollowers(ctx, tx, key, leaderRunID, leaderNodeID)
	if err != nil {
		return nil, err
	}
	if err := txCommitChecked(ctx, tx, time.Now().UnixNano(), key); err != nil {
		return nil, err
	}
	return out, nil
}

// txDrainCoalesceFollowers returns and deletes the coalesce waiters
// parked behind the given leader -- the single definition of "resolve
// this leader's followers", shared by ReleaseAndNotify and
// ResolveCoalesceFollowers.
func txDrainCoalesceFollowers(ctx context.Context, tx *storeTx, key, leaderRunID, leaderNodeID string) ([]ConcurrencyWaiter, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+waiterColumns+`
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
		if _, err := txDeleteWaiter(ctx, tx, w.Key, w.RunID, w.NodeID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// txPark parks an arrival as a waiter -- the single site that writes
// into concurrency_waiters. Re-parking the same (key, run, node) is an
// upsert, so a re-arrival after crash or redelivery refreshes its row
// instead of erroring; its arrival order deliberately resets (the
// re-arrival is a new wait).
func txPark(ctx context.Context, tx *storeTx, w ConcurrencyWaiter, arrivedNS int64) error {
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO concurrency_waiters
		   (key, run_id, node_id, holder_id, arrived_at, policy, cache_key_hash, leader_run_id, leader_node_id, cancel_timeout_ns, cost, declared_capacity)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		w.Key, w.RunID, w.NodeID, w.HolderID, arrivedNS, w.Policy, w.CacheKeyHash,
		w.LeaderRunID, w.LeaderNodeID, int64(w.CancelTimeout), w.Cost, w.DeclaredCapacity,
	)
	return err
}

// txSupersede marks a holder evicted by a CancelOthers arrival -- the
// single site that flips superseded. The row keeps its budget weight
// out of the accounting from this point; the victim drains
// cooperatively and the row is reclaimed or reaped later.
func txSupersede(ctx context.Context, tx *storeTx, key, holderID string) error {
	_, err := tx.ExecContext(
		ctx,
		`UPDATE concurrency_holders SET superseded = 1 WHERE key = ? AND holder_id = ?`,
		key, holderID,
	)
	return err
}

// txDeleteHolder removes one holder row by its primary key -- the
// single by-id DELETE site for concurrency_holders (release and the
// reap sweeps). CancelWaiter reclaims by participant instead, the one
// other holder-delete site.
func txDeleteHolder(ctx context.Context, tx *storeTx, key, holderID string) error {
	_, err := tx.ExecContext(
		ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID,
	)
	return err
}

// txDeleteWaiter removes one waiter row by its primary key -- the
// single DELETE site for concurrency_waiters. Reports whether a row
// matched.
func txDeleteWaiter(ctx context.Context, tx *storeTx, key, runID, nodeID string) (bool, error) {
	res, err := tx.ExecContext(
		ctx,
		`DELETE FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
		key, runID, nodeID,
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
		`SELECT 1 FROM concurrency_entries WHERE key = ?`+s.forUpdate(), key,
	).Scan(&hasEntry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, txCommitChecked(ctx, tx, time.Now().UnixNano(), key)
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
	if err := txCommitChecked(ctx, tx, nowNS, key); err != nil {
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
	// LeaderFailureReason is the leader's categorized failure reason,
	// carried alongside a Failed LeaderOutcome so the follower records the
	// same reason rather than uncategorized.
	LeaderFailureReason string

	// Position and Holders are populated on WaiterStillWaiting (queue
	// policy) so a poller can refresh its "N ahead, held by X" display
	// against the fully-committed queue -- self-correcting any stale
	// value computed at insert time under simultaneous arrival.
	Position    int
	QueueLength int
	Holders     []ConcurrencyHolder
}

// ResolveWaiter is the read-side for polling; never inserts waiter
// rows. cacheKeyHash="" disables memo lookup; leader_* empty for
// queue/cancel_others waiters. bypassRead skips the memo lookup so a
// --no-cache follower waits for the leader instead of replaying a stale
// entry, mirroring the acquire path's BypassRead.
func (s *Store) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (WaiterResolution, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return WaiterResolution{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: clock read after BEGIN so liveness answers match what the
	// serialized writers committed, not a pre-wait snapshot.
	nowNS := time.Now().UnixNano()
	if err := txLockEntry(ctx, tx, s.forUpdate(), key); err != nil {
		return WaiterResolution{}, err
	}
	if _, err := txReapTerminalConcurrencyHolders(ctx, tx, key); err != nil {
		return WaiterResolution{}, err
	}

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

	hit, err := txCacheLookup(ctx, tx, key, cacheKeyHash, nowNS, bypassRead, false)
	if err != nil {
		return WaiterResolution{}, err
	}
	if hit != nil {
		if err := tx.Commit(); err != nil {
			return WaiterResolution{}, err
		}
		return WaiterResolution{
			Status:       WaiterCached,
			OutputRef:    hit.OutputRef,
			OriginRunID:  hit.OriginRunID,
			OriginNodeID: hit.OriginNodeID,
		}, nil
	}

	var waiterArrivedNS int64
	var waiterPolicy string
	err = tx.QueryRowContext(
		ctx,
		`SELECT arrived_at, policy FROM concurrency_waiters WHERE key = ? AND run_id = ? AND node_id = ?`,
		key, runID, nodeID,
	).Scan(&waiterArrivedNS, &waiterPolicy)
	if err == nil {
		shouldPromote := waiterPolicy == OnLimitQueue
		if waiterPolicy == OnLimitCancelOthers {
			var holderCount int
			if err := tx.QueryRowContext(
				ctx,
				`SELECT COUNT(*) FROM concurrency_holders WHERE key = ?`,
				key,
			).Scan(&holderCount); err != nil {
				return WaiterResolution{}, err
			}
			shouldPromote = holderCount == 0
		}
		if shouldPromote {
			now := time.Now()
			promoted, err := txPromotePollingWaiter(ctx, tx, key, runID, nodeID, now.UnixNano(), now.Add(DefaultConcurrencyLease).UnixNano())
			if err != nil {
				return WaiterResolution{}, err
			}
			for _, waiter := range promoted {
				if waiter.RunID == runID && waiter.NodeID == nodeID {
					if err := txCommitChecked(ctx, tx, now.UnixNano(), key); err != nil {
						return WaiterResolution{}, err
					}
					return WaiterResolution{
						Status:             WaiterPromoted,
						HolderID:           waiter.HolderID,
						HolderLeaseExpires: now.Add(DefaultConcurrencyLease),
					}, nil
				}
			}
		}
		var position int
		if e := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE key = ? AND policy IN (?, ?) AND arrived_at < ?`,
			key, OnLimitQueue, OnLimitCancelOthers, waiterArrivedNS,
		).Scan(&position); e != nil {
			return WaiterResolution{}, e
		}
		var queueLength int
		if e := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE key = ? AND policy IN (?, ?)`,
			key, OnLimitQueue, OnLimitCancelOthers,
		).Scan(&queueLength); e != nil {
			return WaiterResolution{}, e
		}
		holders, e := txActiveHolders(ctx, tx, key, nowNS)
		if e != nil {
			return WaiterResolution{}, e
		}
		if err := tx.Commit(); err != nil {
			return WaiterResolution{}, err
		}
		return WaiterResolution{Status: WaiterStillWaiting, Position: position, QueueLength: queueLength, Holders: holders}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WaiterResolution{}, err
	}

	if leaderRunID != "" {
		var leaderOutcome, leaderReason string
		err := tx.QueryRowContext(
			ctx,
			`SELECT outcome, failure_reason FROM nodes WHERE run_id = ? AND node_id = ?`,
			leaderRunID, leaderNodeID,
		).Scan(&leaderOutcome, &leaderReason)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return WaiterResolution{}, err
		}
		if err := tx.Commit(); err != nil {
			return WaiterResolution{}, err
		}
		return WaiterResolution{
			Status:              WaiterLeaderFinished,
			LeaderRunID:         leaderRunID,
			LeaderNodeID:        leaderNodeID,
			LeaderOutcome:       leaderOutcome,
			LeaderFailureReason: leaderReason,
		}, nil
	}

	if err := tx.Commit(); err != nil {
		return WaiterResolution{}, err
	}
	return WaiterResolution{Status: WaiterCancelled}, nil
}

// CancelWaiter removes one waiter row; returns whether one matched.
func (s *Store) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: entries-row lock first; the cancel may reclaim a holder and
	// promote, which must serialize with concurrent admissions.
	if err := txLockEntry(ctx, tx, s.forUpdate(), key); err != nil {
		return false, err
	}

	waiterDeleted, err := txDeleteWaiter(ctx, tx, key, runID, nodeID)
	if err != nil {
		return false, err
	}

	hres, err := tx.ExecContext(
		ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND run_id = ? AND node_id = ?`,
		key, runID, nodeID,
	)
	if err != nil {
		return false, err
	}
	hn, err := hres.RowsAffected()
	if err != nil {
		return false, err
	}
	if hn > 0 {
		now := time.Now()
		if _, err := txPromoteWaiters(ctx, tx, key, now.UnixNano(), now.Add(DefaultConcurrencyLease).UnixNano()); err != nil {
			return false, err
		}
	}
	if err := txCommitChecked(ctx, tx, time.Now().UnixNano(), key); err != nil {
		return false, err
	}
	return waiterDeleted || hn > 0, nil
}

// GetConcurrencyState returns capacity + holders + waiters; ErrNotFound
// when undeclared. UsedCost and EffectiveCapacity are derived through
// the same accounting rules the admission path enforces
// (holderCountsForBudget + declaredCapacityFloorTerm), inside one
// transaction, so the operator view cannot drift from what admission
// actually does. Parked waiters are excluded from the floor: a
// non-admitted waiter holds no budget, so folding its declaration would
// report a capacity below what the live holders actually enforce.
func (s *Store) GetConcurrencyState(ctx context.Context, key string) (*ConcurrencyState, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var capacity int
	err = tx.QueryRowContext(
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
	hrows, err := tx.QueryContext(
		ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders WHERE key = ? ORDER BY claimed_at ASC`, key,
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
			state.UsedCost += holderBudgetCost(h.Cost, h.DeclaredCapacity)
			if t, ok := holderFloorTerm(h.Cost, h.DeclaredCapacity); ok && (floor == 0 || t < floor) {
				floor = t
			}
		}
		scrubInheritedHolder(&h)
		state.Holders = append(state.Holders, h)
	}
	_ = hrows.Close()
	if err := hrows.Err(); err != nil {
		return nil, err
	}

	wrows, err := tx.QueryContext(
		ctx,
		`SELECT `+waiterColumns+`
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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return state, nil
}

// txReapTerminalConcurrencyHolders removes holders whose run already has a
// terminal row. A missing run row is not proof of death; absence falls back
// to the lease-expiry reaper.
func txReapTerminalConcurrencyHolders(ctx context.Context, tx *storeTx, key string) ([]ConcurrencyHolder, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+prefixColumns(holderColumns, "h.")+`
		   FROM concurrency_holders h
		   JOIN runs r ON r.id = h.run_id
		  WHERE h.key = ?
		    AND r.finished_at IS NOT NULL`,
		key,
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
		if _, _, _, err := txReleaseHolder(ctx, tx, holder.Key, holder.HolderID, "failed", "", "", 0); err != nil {
			return nil, err
		}
	}
	return stale, nil
}

// reapStaleConcurrencyHolders deletes lease-expired holders; caller
// runs PromoteNextWaiters and emits audit events. The transaction
// holds the read locks (FOR UPDATE SKIP LOCKED on Postgres) for the
// duration so concurrent reapers pick disjoint rows.
func (s *Store) reapStaleConcurrencyHolders(ctx context.Context) ([]ConcurrencyHolder, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: clock read after BEGIN; see AcquireConcurrencySlot.
	now := time.Now().UnixNano()

	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders WHERE lease_expires_at <= ?`+s.forUpdateSkipLocked(), now,
	)
	if err != nil {
		return nil, err
	}
	var stale []ConcurrencyHolder
	for rows.Next() {
		h, err := scanHolder(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		stale = append(stale, h)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range stale {
		if err := txDeleteHolder(ctx, tx, h.Key, h.HolderID); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(stale))
	for _, h := range stale {
		keys = append(keys, h.Key)
	}
	if err := txCommitChecked(ctx, tx, now, keys...); err != nil {
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

	// safety: entries-row lock first; dropping superseded rows changes
	// what a concurrent promotion may reclaim.
	if err := txLockEntry(ctx, tx, s.forUpdate(), key); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders WHERE key = ? AND superseded = 1`, key,
	)
	if err != nil {
		return nil, err
	}
	var out []ConcurrencyHolder
	for rows.Next() {
		h, err := scanHolder(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, h)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range out {
		if err := txDeleteHolder(ctx, tx, h.Key, h.HolderID); err != nil {
			return nil, err
		}
	}
	if err := txCommitChecked(ctx, tx, time.Now().UnixNano(), key); err != nil {
		return nil, err
	}
	return out, nil
}

// reapStaleConcurrencyWaiters drops orphan coalesce followers (leader
// gone) and old waiters whose owning run is not live.
func (s *Store) reapStaleConcurrencyWaiters(ctx context.Context, maxAge time.Duration) ([]ConcurrencyWaiter, error) {
	if maxAge <= 0 {
		return nil, nil
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	nowNS := now.UnixNano()
	cutoff := now.Add(-maxAge).UnixNano()
	heartbeatCutoff := now.Add(-DefaultLeaseDuration).UnixNano()

	orphanRows, err := tx.QueryContext(
		ctx,
		`SELECT `+prefixColumns(waiterColumns, "w.")+`
		   FROM concurrency_waiters w
		  WHERE w.policy = ?
		    AND w.leader_run_id <> ''
		    AND NOT EXISTS (
		      SELECT 1 FROM concurrency_holders h
		       WHERE h.key = w.key
		         AND h.run_id = w.leader_run_id
		         AND h.node_id = w.leader_node_id
		         AND `+holderLiveSQL("h.")+`
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

	ageRows, err := tx.QueryContext(
		ctx,
		`SELECT `+prefixColumns(waiterColumns, "w.")+`
		   FROM concurrency_waiters w
		  WHERE w.arrived_at < ?
		    AND NOT EXISTS (
		      SELECT 1 FROM runs r
		       WHERE r.id = w.run_id
		         AND r.status = ?
		         AND r.last_heartbeat_at >= ?
		    )`+s.forUpdateSkipLocked(),
		cutoff, runStatusRunning, heartbeatCutoff,
	)
	if err != nil {
		return nil, err
	}
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
		if _, err := txDeleteWaiter(ctx, tx, w.Key, w.RunID, w.NodeID); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(dropped))
	for _, w := range dropped {
		keys = append(keys, w.Key)
	}
	if err := txCommitChecked(ctx, tx, nowNS, keys...); err != nil {
		return nil, err
	}
	return dropped, nil
}

// prefixColumns prefixes each column in a comma-separated canonical
// column list with a table alias so the list stays usable in aliased
// joins without re-spelling it.
func prefixColumns(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
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
	// hack: composite PK in IN-subselect avoids rowid/ctid dialect branching.
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

// holderColumns is the canonical SELECT column list for scanHolder, in
// the exact order it scans. Centralized so the cost /
// declared_capacity tail can't drift between call sites.
const holderColumns = `key, holder_id, run_id, node_id, claimed_at, queue_arrived_at, lease_expires_at, superseded, cost, declared_capacity`

func scanHolder(rs rowScanner) (ConcurrencyHolder, error) {
	var h ConcurrencyHolder
	var claimedNS, queueArrivedNS, expiresNS int64
	var superInt int
	if err := rs.Scan(&h.Key, &h.HolderID, &h.RunID, &h.NodeID, &claimedNS, &queueArrivedNS, &expiresNS, &superInt, &h.Cost, &h.DeclaredCapacity); err != nil {
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
