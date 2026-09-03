package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const nodeClaimOfferWindow = 5 * time.Second

// NodeSchedulingSummary is the information an executor needs before it
// reserves capacity for an offer. Zero resources ask admission to apply its
// conservative default.
type NodeSchedulingSummary struct {
	RunID                string     `json:"run_id"`
	NodeID               string     `json:"node_id"`
	NeedsLabels          []string   `json:"needs_labels,omitempty"`
	PrefersLabels        []string   `json:"prefers_labels,omitempty"`
	RequestedCores       float64    `json:"requested_cores,omitempty"`
	RequestedMemoryBytes int64      `json:"requested_memory_bytes,omitempty"`
	RequestedSlots       int        `json:"requested_slots"`
	OfferDeadline        *time.Time `json:"offer_deadline,omitempty"`
}

// NodeClaimResolution is a trusted scheduling decision for one executor and
// node. Registration and policy code resolves it before the store compares
// offers.
type NodeClaimResolution struct {
	WorkerID          string
	ExecutorKind      string
	ReservationID     string
	BasePriority      int
	EffectivePriority int
}

// NodeClaimResolver snapshots registration, capacity, and scheduling policy
// before claim arbitration enters its transaction. Resolve must not perform I/O.
type NodeClaimResolver interface {
	Resolve(*Node) (NodeClaimResolution, bool)
}

// NodeClaimResolverFunc adapts a pure function to NodeClaimResolver.
type NodeClaimResolverFunc func(*Node) (NodeClaimResolution, bool)

func (f NodeClaimResolverFunc) Resolve(n *Node) (NodeClaimResolution, bool) {
	return f(n)
}

// NodeClaimOfferResult distinguishes an active offer from an empty queue.
type NodeClaimOfferResult struct {
	Node    *Node
	Pending bool
}

// PrepareNextNodeClaim returns the oldest node this executor may reserve for.
// The returned summary is advisory until OfferNodeClaim commits against the
// same live node and reservation.
func (s *Store) PrepareNextNodeClaim(ctx context.Context, resolver NodeClaimResolver) (*NodeSchedulingSummary, error) {
	rows, err := s.query(ctx, `
SELECT `+nodeSelectColumns+`
 FROM nodes
 WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND `+nodeNotDone+`
 ORDER BY ready_at ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		n := &Node{}
		if err := scanNodeRow(rows, n); err != nil {
			return nil, err
		}
		if _, ok := resolver.Resolve(n); !ok {
			continue
		}
		summary := &NodeSchedulingSummary{
			RunID:                n.RunID,
			NodeID:               n.NodeID,
			NeedsLabels:          append([]string(nil), n.NeedsLabels...),
			PrefersLabels:        append([]string(nil), n.PrefersLabels...),
			RequestedCores:       n.RequestedCores,
			RequestedMemoryBytes: n.RequestedMemoryBytes,
			RequestedSlots:       n.RequestedSlots,
		}
		if n.OfferStartedAt != nil {
			deadline := n.OfferStartedAt.Add(nodeClaimOfferWindow)
			summary.OfferDeadline = &deadline
		}
		return summary, nil
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, ErrNotFound
}

// OfferNodeClaim records a capacity-backed offer and atomically awards the
// node when the recorded ceiling arrives or the five-second round ends.
func (s *Store) OfferNodeClaim(
	ctx context.Context,
	claimant ClaimIdentity,
	holderID, runID, nodeID string,
	lease time.Duration,
	resolver NodeClaimResolver,
) (NodeClaimOfferResult, error) {
	if holderID == "" || runID == "" || nodeID == "" {
		return NodeClaimOfferResult{}, errors.New("node claim offer requires holder, run, and node ids")
	}
	lease = clampNodeLease(lease)
	now := time.Now()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return NodeClaimOfferResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	claimed, err := claimedNodeForOffer(ctx, tx, claimant, holderID, now)
	if err == nil {
		resolution, ok := resolver.Resolve(claimed)
		if ok && resolution.ReservationID == claimed.ClaimReservationID {
			if err := tx.Commit(); err != nil {
				return NodeClaimOfferResult{}, err
			}
			return NodeClaimOfferResult{Node: claimed}, nil
		}
	} else if !errors.Is(err, ErrNotFound) {
		return NodeClaimOfferResult{}, err
	}

	n := &Node{}
	err = scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE run_id = ? AND node_id = ? AND ready_at IS NOT NULL
   AND claimed_by IS NULL AND `+nodeNotDone+tx.forUpdate(), runID, nodeID), n)
	if errors.Is(err, ErrNotFound) {
		if _, derr := tx.ExecContext(ctx, `DELETE FROM node_claim_offers
 WHERE claim_token_prefix = ? AND claim_principal = ? AND holder_id = ?`,
			claimant.TokenPrefix, claimant.Principal, holderID); derr != nil {
			return NodeClaimOfferResult{}, derr
		}
		if err := tx.Commit(); err != nil {
			return NodeClaimOfferResult{}, err
		}
		return NodeClaimOfferResult{}, nil
	}
	if err != nil {
		return NodeClaimOfferResult{}, err
	}
	resolution, eligible := resolver.Resolve(n)
	if !eligible {
		if err := tx.Commit(); err != nil {
			return NodeClaimOfferResult{}, err
		}
		return NodeClaimOfferResult{}, nil
	}
	if err := validateNodeClaimResolution(resolution); err != nil {
		return NodeClaimOfferResult{}, err
	}

	offeredAt := now.UnixNano()
	var priorRunID, priorNodeID, priorReservationID string
	var priorOfferedAt, priorLastSeen, priorLeaseNS int64
	priorErr := tx.QueryRowContext(ctx, `SELECT run_id, node_id, reservation_id, offered_at, last_seen_at, lease_ns
  FROM node_claim_offers
 WHERE claim_token_prefix = ? AND claim_principal = ? AND holder_id = ?`+tx.forUpdate(),
		claimant.TokenPrefix, claimant.Principal, holderID,
	).Scan(&priorRunID, &priorNodeID, &priorReservationID, &priorOfferedAt, &priorLastSeen, &priorLeaseNS)
	if priorErr == nil && priorRunID == runID && priorNodeID == nodeID &&
		priorReservationID == resolution.ReservationID && priorLastSeen+priorLeaseNS >= now.UnixNano() {
		offeredAt = priorOfferedAt
	} else if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
		return NodeClaimOfferResult{}, priorErr
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_claim_offers
       (claim_token_prefix, claim_principal, holder_id, run_id, node_id,
        worker_id, executor_kind, reservation_id, base_priority, effective_priority,
        offered_at, last_seen_at, lease_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (claim_token_prefix, claim_principal, holder_id) DO UPDATE SET
       run_id = excluded.run_id,
       node_id = excluded.node_id,
       worker_id = excluded.worker_id,
       executor_kind = excluded.executor_kind,
       reservation_id = excluded.reservation_id,
       base_priority = excluded.base_priority,
       effective_priority = excluded.effective_priority,
       offered_at = excluded.offered_at,
       last_seen_at = excluded.last_seen_at,
       lease_ns = excluded.lease_ns`,
		claimant.TokenPrefix, claimant.Principal, holderID, runID, nodeID,
		resolution.WorkerID, resolution.ExecutorKind, resolution.ReservationID,
		resolution.BasePriority, resolution.EffectivePriority, offeredAt, now.UnixNano(), int64(lease)); err != nil {
		return NodeClaimOfferResult{}, err
	}

	due := resolution.EffectivePriority == 100 ||
		resolution.EffectivePriority >= n.OfferPriorityCeiling ||
		(n.OfferStartedAt != nil && !now.Before(n.OfferStartedAt.Add(nodeClaimOfferWindow)))
	if !due {
		if err := tx.Commit(); err != nil {
			return NodeClaimOfferResult{}, err
		}
		return NodeClaimOfferResult{Pending: true}, nil
	}

	winner, err := awardBestNodeClaimOffer(ctx, tx, runID, nodeID, now)
	if err != nil {
		return NodeClaimOfferResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return NodeClaimOfferResult{}, err
	}
	if winner.claimant == claimant && winner.holderID == holderID {
		awarded, err := s.GetNode(ctx, runID, nodeID)
		if err != nil {
			return NodeClaimOfferResult{}, err
		}
		return NodeClaimOfferResult{Node: awarded}, nil
	}
	return NodeClaimOfferResult{}, nil
}

type nodeClaimOfferWinner struct {
	claimant          ClaimIdentity
	holderID          string
	workerID          string
	executorKind      string
	reservationID     string
	basePriority      int
	effectivePriority int
	lease             time.Duration
}

func awardBestNodeClaimOffer(ctx context.Context, tx *storeTx, runID, nodeID string, now time.Time) (nodeClaimOfferWinner, error) {
	var winner nodeClaimOfferWinner
	var leaseNS int64
	err := tx.QueryRowContext(ctx, `
SELECT claim_principal, claim_token_prefix, holder_id, worker_id, executor_kind,
       reservation_id, base_priority, effective_priority, lease_ns
 FROM node_claim_offers
	WHERE run_id = ? AND node_id = ? AND last_seen_at + lease_ns >= ?
 ORDER BY effective_priority DESC, offered_at ASC, worker_id ASC,
          claim_token_prefix ASC, claim_principal ASC, holder_id ASC
	LIMIT 1`+tx.forUpdate(), runID, nodeID, now.UnixNano()).Scan(
		&winner.claimant.Principal, &winner.claimant.TokenPrefix, &winner.holderID,
		&winner.workerID, &winner.executorKind, &winner.reservationID,
		&winner.basePriority, &winner.effectivePriority, &leaseNS)
	if errors.Is(err, sql.ErrNoRows) {
		return winner, ErrNotFound
	}
	if err != nil {
		return winner, err
	}
	winner.lease = clampNodeLease(time.Duration(leaseNS))
	expires := now.Add(winner.lease)
	res, err := tx.ExecContext(ctx, `
UPDATE nodes
   SET claimed_by = ?, claim_principal = ?, claim_token_prefix = ?,
       lease_expires_at = ?, claim_base_priority = ?, claim_priority = ?,
       claim_worker_id = ?, claim_executor_kind = ?, claim_reservation_id = ?
 WHERE run_id = ? AND node_id = ? AND ready_at IS NOT NULL
   AND claimed_by IS NULL AND `+nodeNotDone,
		winner.holderID, winner.claimant.Principal, winner.claimant.TokenPrefix,
		expires.UnixNano(), winner.basePriority, winner.effectivePriority,
		winner.workerID, winner.executorKind, winner.reservationID, runID, nodeID)
	if err != nil {
		return winner, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return winner, err
	}
	if changed != 1 {
		return winner, ErrLockHeld
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM node_claim_offers WHERE run_id = ? AND node_id = ?`, runID, nodeID); err != nil {
		return winner, err
	}
	return winner, nil
}

func claimedNodeForOffer(ctx context.Context, tx *storeTx, claimant ClaimIdentity, holderID string, now time.Time) (*Node, error) {
	n := &Node{}
	err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE claimed_by = ? AND claim_principal = ? AND claim_token_prefix = ?
   AND `+nodeClaimLiveSQL+` AND `+nodeNotDone+`
 ORDER BY lease_expires_at DESC
 LIMIT 1`+tx.forUpdate(), holderID, claimant.Principal, claimant.TokenPrefix, now.UnixNano()), n)
	return n, err
}

func validateNodeClaimResolution(r NodeClaimResolution) error {
	if r.WorkerID == "" {
		return errors.New("node claim resolution requires worker id")
	}
	if r.ReservationID == "" {
		return errors.New("node claim resolution requires reservation id")
	}
	if r.BasePriority < 0 || r.BasePriority > 100 {
		return fmt.Errorf("node claim base priority %d: expected 0 through 100", r.BasePriority)
	}
	if r.EffectivePriority < 0 || r.EffectivePriority > 100 {
		return fmt.Errorf("node claim effective priority %d: expected 0 through 100", r.EffectivePriority)
	}
	return nil
}

// FinalizeNodeReady awards the best pending offer or revokes the node for a
// local or cloud fallback in one transaction. Revoked reports that fallback
// owns the transition.
func (s *Store) FinalizeNodeReady(ctx context.Context, runID, nodeID string) (revoked bool, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var readyAt sql.NullInt64
	var claimedBy sql.NullString
	var status string
	err = tx.QueryRowContext(ctx, `SELECT ready_at, claimed_by, status FROM nodes
 WHERE run_id = ? AND node_id = ?`+tx.forUpdate(), runID, nodeID).Scan(&readyAt, &claimedBy, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if !readyAt.Valid || claimedBy.Valid || status == nodeStatusDone {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_claim_offers
	 WHERE run_id = ? AND node_id = ? AND last_seen_at + lease_ns < ?`, runID, nodeID, now.UnixNano()); err != nil {
		return false, err
	}
	var offers int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_claim_offers WHERE run_id = ? AND node_id = ?`, runID, nodeID).Scan(&offers); err != nil {
		return false, err
	}
	if offers > 0 {
		if _, err := awardBestNodeClaimOffer(ctx, tx, runID, nodeID, now); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE nodes
   SET ready_at = NULL, offer_started_at = NULL
 WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL AND `+nodeNotDone, runID, nodeID)
	if err != nil {
		return false, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed == 1, nil
}
