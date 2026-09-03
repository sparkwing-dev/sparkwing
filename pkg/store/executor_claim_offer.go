package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	nodeClaimOfferWindow  = 5 * time.Second
	executorOfferLiveness = 2 * time.Second
)

// ExecutorClaimPreparation is the controller-owned admission contract for one
// enrolled executor and ready node.
type ExecutorClaimPreparation struct {
	Summary       ExecutorSchedulingSummary  `json:"summary"`
	Membership    ExecutorMembershipSnapshot `json:"membership"`
	OfferDeadline *time.Time                 `json:"offer_deadline,omitempty"`
}

// ExecutorClaimOffer binds one controller offer to capacity already reserved
// under the exact preparation digest.
type ExecutorClaimOffer struct {
	ExecutorName   string
	HolderID       string
	RunID          string
	NodeID         string
	ReservationID  string
	ResourceDigest string
	Slot           int
	Lease          time.Duration
}

// ExecutorClaimOfferResult distinguishes an unresolved round from an offer
// that lost or no longer names a ready node.
type ExecutorClaimOfferResult struct {
	Node    *Node
	Pending bool
}

// ExecutorClaimRoundResult reports whether the coordinator may take fallback
// ownership or must keep waiting for the five-second round.
type ExecutorClaimRoundResult struct {
	Revoked bool
	Pending bool
}

// PrepareNextExecutorClaim returns the oldest eligible node without changing
// queue or claim state. The exact resource digest is recomputed on award.
func (s *Store) PrepareNextExecutorClaim(ctx context.Context, claimant ClaimIdentity, executorName string) (*ExecutorClaimPreparation, error) {
	executor, err := s.ExecutorForCredential(ctx, claimant, executorName)
	if err != nil {
		return nil, err
	}
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return nil, err
	}
	activeAfter := time.Now().Add(-executorOfferLiveness).UnixNano()
	type candidate struct {
		runID, nodeID string
		readyAt       int64
	}
	var after candidate
	for {
		rows, err := s.query(ctx, `
SELECT n.run_id, n.node_id, n.ready_at
 FROM nodes n
 WHERE n.ready_at IS NOT NULL AND n.claimed_by IS NULL AND n.`+nodeNotDone+`
	   AND NOT EXISTS (
	       SELECT 1 FROM node_claim_offers o
	        WHERE o.run_id = n.run_id AND o.node_id = n.node_id
	          AND o.executor_name = ? AND o.last_seen_at >= ?)
   AND (n.ready_at > ? OR (n.ready_at = ? AND (n.run_id > ? OR (n.run_id = ? AND n.node_id > ?))))
 ORDER BY n.ready_at, n.run_id, n.node_id
		LIMIT 64`, executorName, activeAfter, after.readyAt, after.readyAt, after.runID, after.runID, after.nodeID)
		if err != nil {
			return nil, err
		}
		page := make([]candidate, 0, 64)
		for rows.Next() {
			var item candidate
			if err := rows.Scan(&item.runID, &item.nodeID, &item.readyAt); err != nil {
				_ = rows.Close()
				return nil, err
			}
			page = append(page, item)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		for _, item := range page {
			summary, err := s.SchedulingSummary(ctx, item.runID, item.nodeID)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			membership, err := s.ResolveExecutorMembership(ctx, claimant, executorName, summary)
			if err != nil {
				return nil, err
			}
			if !membership.Eligible {
				continue
			}
			node, err := s.GetNode(ctx, item.runID, item.nodeID)
			if err != nil {
				return nil, err
			}
			if node.AvoidUntil != nil && node.AvoidUntil.After(time.Now()) &&
				node.AvoidCoordinatorID == coordinatorID && node.AvoidExecutorKind == executor.Kind && node.AvoidExecutorID == executor.Name {
				alternate, err := s.hasAlternateEligibleExecutor(ctx, executor.Name, summary)
				if err != nil {
					return nil, err
				}
				if alternate {
					continue
				}
			}
			preparation := &ExecutorClaimPreparation{Summary: summary, Membership: membership}
			var opened sql.NullInt64
			if err := s.queryRow(ctx, `SELECT offer_started_at FROM nodes WHERE run_id = ? AND node_id = ?`, item.runID, item.nodeID).Scan(&opened); err != nil {
				return nil, err
			}
			if opened.Valid {
				deadline := time.Unix(0, opened.Int64).Add(nodeClaimOfferWindow)
				preparation.OfferDeadline = &deadline
			}
			return preparation, nil
		}
		if len(page) < 64 {
			return nil, ErrNotFound
		}
		after = page[len(page)-1]
	}
}

func (s *Store) hasAlternateEligibleExecutor(ctx context.Context, current string, summary ExecutorSchedulingSummary) (bool, error) {
	executors, err := s.ListExecutors(ctx)
	if err != nil {
		return false, err
	}
	activeAfter := time.Now().Add(-ExecutorRegistrationActiveWindow)
	for _, candidate := range executors {
		if candidate.Name == current || candidate.LastSeen.Before(activeAfter) {
			continue
		}
		_, eligible, err := s.executorEligible(ctx, candidate, summary)
		if err != nil {
			return false, err
		}
		if eligible {
			return true, nil
		}
	}
	return false, nil
}

// OfferExecutorClaim records one live reservation and awards immediately at
// the round ceiling or deterministically at the deadline.
func (s *Store) OfferExecutorClaim(ctx context.Context, claimant ClaimIdentity, offer ExecutorClaimOffer) (ExecutorClaimOfferResult, error) {
	return s.offerExecutorClaimAt(ctx, claimant, offer, time.Now())
}

func (s *Store) offerExecutorClaimAt(ctx context.Context, claimant ClaimIdentity, offer ExecutorClaimOffer, now time.Time) (ExecutorClaimOfferResult, error) {
	if offer.ExecutorName == "" || offer.HolderID == "" || offer.RunID == "" || offer.NodeID == "" ||
		offer.ReservationID == "" || offer.ResourceDigest == "" || offer.Slot < 0 {
		return ExecutorClaimOfferResult{}, errors.New("executor offer requires executor, holder, node, reservation, digest, and slot")
	}
	offer.Lease = clampNodeLease(offer.Lease)
	if err := s.ValidateExecutorClaimReservation(ctx, claimant, offer.RunID, offer.NodeID,
		offer.ExecutorName, offer.ReservationID, offer.Slot, offer.ResourceDigest); err == nil {
		n, err := s.GetNode(ctx, offer.RunID, offer.NodeID)
		if err != nil {
			return ExecutorClaimOfferResult{}, err
		}
		if n.ClaimedBy == offer.HolderID {
			return ExecutorClaimOfferResult{Node: n}, nil
		}
	}
	summary, err := s.SchedulingSummary(ctx, offer.RunID, offer.NodeID)
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if summary.ResourceDigest != offer.ResourceDigest {
		return ExecutorClaimOfferResult{}, ErrLockHeld
	}
	membership, err := s.ResolveExecutorMembership(ctx, claimant, offer.ExecutorName, summary)
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if !membership.Eligible || offer.Slot >= membership.MaxConcurrent {
		return ExecutorClaimOfferResult{}, ErrNotFound
	}

	tx, err := s.beginTx(ctx)
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if n, ok, err := claimedExecutorOffer(ctx, tx, claimant, offer, now); err != nil {
		return ExecutorClaimOfferResult{}, err
	} else if ok {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimOfferResult{}, err
		}
		return ExecutorClaimOfferResult{Node: n}, nil
	}

	n := &Node{}
	err = scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes
 WHERE run_id = ? AND node_id = ? AND ready_at IS NOT NULL
   AND claimed_by IS NULL AND `+nodeNotDone+tx.forUpdate(), offer.RunID, offer.NodeID), n)
	if errors.Is(err, ErrNotFound) {
		_, _ = tx.ExecContext(ctx, `DELETE FROM node_claim_offers
 WHERE claim_token_prefix = ? AND claim_principal = ? AND holder_id = ?`, claimant.TokenPrefix, claimant.Principal, offer.HolderID)
		if err := tx.Commit(); err != nil {
			return ExecutorClaimOfferResult{}, err
		}
		return ExecutorClaimOfferResult{}, nil
	}
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_claim_offers
 WHERE last_seen_at < ? AND (reservation_id = ? OR (executor_name = ? AND slot = ?)
        OR (executor_name = ? AND run_id = ? AND node_id = ?))`,
		now.Add(-executorOfferLiveness).UnixNano(), offer.ReservationID, offer.ExecutorName, offer.Slot,
		offer.ExecutorName, offer.RunID, offer.NodeID); err != nil {
		return ExecutorClaimOfferResult{}, err
	}

	var conflicts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_claim_offers
 WHERE (reservation_id = ? OR (executor_name = ? AND slot = ?)
        OR (executor_name = ? AND run_id = ? AND node_id = ?))
   AND NOT (claim_token_prefix = ? AND claim_principal = ? AND holder_id = ?)`,
		offer.ReservationID, offer.ExecutorName, offer.Slot,
		offer.ExecutorName, offer.RunID, offer.NodeID,
		claimant.TokenPrefix, claimant.Principal, offer.HolderID).Scan(&conflicts); err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if conflicts != 0 {
		return ExecutorClaimOfferResult{}, ErrLockHeld
	}

	offeredAt := now.UnixNano()
	var priorRunID, priorNodeID, priorReservationID, priorDigest string
	var priorSlot int
	var priorOfferedAt int64
	priorErr := tx.QueryRowContext(ctx, `SELECT run_id, node_id, reservation_id, resource_digest, slot, offered_at
  FROM node_claim_offers
 WHERE claim_token_prefix = ? AND claim_principal = ? AND holder_id = ?`+tx.forUpdate(),
		claimant.TokenPrefix, claimant.Principal, offer.HolderID).Scan(
		&priorRunID, &priorNodeID, &priorReservationID, &priorDigest, &priorSlot, &priorOfferedAt)
	if priorErr == nil && priorRunID == offer.RunID && priorNodeID == offer.NodeID &&
		priorReservationID == offer.ReservationID && priorDigest == offer.ResourceDigest && priorSlot == offer.Slot {
		offeredAt = priorOfferedAt
	} else if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
		return ExecutorClaimOfferResult{}, priorErr
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_claim_offers
       (claim_token_prefix, claim_principal, holder_id, run_id, node_id,
        executor_name, membership_id, worker_id, executor_kind, reservation_id,
        resource_digest, slot, base_priority, effective_priority, offered_at, last_seen_at, lease_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (claim_token_prefix, claim_principal, holder_id) DO UPDATE SET
       run_id = excluded.run_id, node_id = excluded.node_id,
       executor_name = excluded.executor_name, membership_id = excluded.membership_id,
       worker_id = excluded.worker_id, executor_kind = excluded.executor_kind,
       reservation_id = excluded.reservation_id, resource_digest = excluded.resource_digest,
       slot = excluded.slot, base_priority = excluded.base_priority,
       effective_priority = excluded.effective_priority, offered_at = excluded.offered_at,
       last_seen_at = excluded.last_seen_at, lease_ns = excluded.lease_ns`,
		claimant.TokenPrefix, claimant.Principal, offer.HolderID, offer.RunID, offer.NodeID,
		offer.ExecutorName, membership.MembershipID, membership.WorkerID, membership.Kind, offer.ReservationID,
		offer.ResourceDigest, offer.Slot, membership.RegisteredBasePriority, membership.EffectivePriority,
		offeredAt, now.UnixNano(), int64(offer.Lease)); err != nil {
		if isUniqueViolation(err) {
			return ExecutorClaimOfferResult{}, ErrLockHeld
		}
		return ExecutorClaimOfferResult{}, err
	}

	due := membership.EffectivePriority == 100 || membership.EffectivePriority >= n.OfferPriorityCeiling ||
		(n.OfferStartedAt != nil && !now.Before(n.OfferStartedAt.Add(nodeClaimOfferWindow)))
	if !due {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimOfferResult{}, err
		}
		return ExecutorClaimOfferResult{Pending: true}, nil
	}
	winner, err := s.awardBestExecutorOffer(ctx, tx, offer.RunID, offer.NodeID, now)
	if err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExecutorClaimOfferResult{}, err
	}
	if winner != nil && winner.ExecutorName == offer.ExecutorName && winner.ReservationID == offer.ReservationID && winner.HolderID == offer.HolderID {
		return ExecutorClaimOfferResult{Node: winner.Node}, nil
	}
	return ExecutorClaimOfferResult{}, nil
}

type executorOfferWinner struct {
	ExecutorName, MembershipID, ExecutorKind, HolderID, ReservationID, ResourceDigest string
	Claimant                                                                          ClaimIdentity
	Slot, BasePriority, EffectivePriority                                             int
	Lease                                                                             time.Duration
	Node                                                                              *Node
}

func (s *Store) awardBestExecutorOffer(ctx context.Context, tx *storeTx, runID, nodeID string, now time.Time) (*executorOfferWinner, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT executor_name, membership_id, claim_principal, claim_token_prefix, holder_id,
	   reservation_id, resource_digest, slot, base_priority, effective_priority,
	   executor_kind, lease_ns
  FROM node_claim_offers
 WHERE run_id = ? AND node_id = ? AND last_seen_at >= ?
 ORDER BY effective_priority DESC, offered_at ASC, executor_name ASC, slot ASC, holder_id ASC`+tx.forUpdate(),
		runID, nodeID, now.Add(-executorOfferLiveness).UnixNano())
	if err != nil {
		return nil, err
	}
	var candidates []executorOfferWinner
	for rows.Next() {
		var item executorOfferWinner
		var leaseNS int64
		if err := rows.Scan(&item.ExecutorName, &item.MembershipID, &item.Claimant.Principal, &item.Claimant.TokenPrefix,
			&item.HolderID, &item.ReservationID, &item.ResourceDigest, &item.Slot, &item.BasePriority,
			&item.EffectivePriority, &item.ExecutorKind, &leaseNS); err != nil {
			_ = rows.Close()
			return nil, err
		}
		item.Lease = clampNodeLease(time.Duration(leaseNS))
		candidates = append(candidates, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range candidates {
		item := &candidates[i]
		n, err := s.claimReadyNodeForExecutorTx(ctx, tx, item.Claimant, item.ExecutorName,
			runID, nodeID, item.HolderID, item.Lease, item.ReservationID, item.Slot, item.ResourceDigest)
		if err == nil {
			item.Node = n
			if _, err := tx.ExecContext(ctx, `UPDATE nodes SET
			 claim_base_priority = ?, claim_priority = ?, claim_worker_id = ?, claim_executor_kind = ?,
			 claim_reservation_id = ?
			 WHERE run_id = ? AND node_id = ?`, item.BasePriority, item.EffectivePriority,
				item.ExecutorName, item.ExecutorKind, item.ReservationID, runID, nodeID); err != nil {
				return nil, err
			}
			item.Node.ClaimBasePriority = item.BasePriority
			item.Node.ClaimPriority = item.EffectivePriority
			item.Node.ClaimWorkerID = item.ExecutorName
			item.Node.ClaimExecutorKind = item.ExecutorKind
			item.Node.ClaimReservationID = item.ReservationID
			if _, err := tx.ExecContext(ctx, `DELETE FROM node_claim_offers WHERE run_id = ? AND node_id = ?`, runID, nodeID); err != nil {
				return nil, err
			}
			return item, nil
		}
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrLockHeld) && !errors.Is(err, ErrExecutorCredentialMismatch) {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM node_claim_offers WHERE executor_name = ? AND slot = ?`, item.ExecutorName, item.Slot); err != nil {
			return nil, err
		}
	}
	return nil, ErrNotFound
}

func claimedExecutorOffer(ctx context.Context, tx *storeTx, claimant ClaimIdentity, offer ExecutorClaimOffer, now time.Time) (*Node, bool, error) {
	var holderID, principal, tokenPrefix, executorName, reservationID sql.NullString
	var slot int
	var expires int64
	err := tx.QueryRowContext(ctx, `SELECT claimed_by, claim_principal, claim_token_prefix,
       claim_executor, claim_reservation, claim_slot, COALESCE(lease_expires_at, 0)
  FROM nodes WHERE run_id = ? AND node_id = ?`, offer.RunID, offer.NodeID).Scan(
		&holderID, &principal, &tokenPrefix, &executorName, &reservationID, &slot, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if holderID.String != offer.HolderID || principal.String != claimant.Principal || tokenPrefix.String != claimant.TokenPrefix ||
		executorName.String != offer.ExecutorName || reservationID.String != offer.ReservationID || slot != offer.Slot || expires < now.UnixNano() {
		return nil, false, nil
	}
	n := &Node{}
	if err := scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeSelectColumns+`
  FROM nodes WHERE run_id = ? AND node_id = ?`, offer.RunID, offer.NodeID), n); err != nil {
		return nil, false, err
	}
	return n, true, nil
}

// FinalizeExecutorClaimRound awards the best live offer after the deadline or
// atomically transfers the still-unclaimed node to coordinator fallback.
func (s *Store) FinalizeExecutorClaimRound(ctx context.Context, runID, nodeID string) (ExecutorClaimRoundResult, error) {
	return s.finalizeExecutorClaimRoundAt(ctx, runID, nodeID, time.Now())
}

func (s *Store) finalizeExecutorClaimRoundAt(ctx context.Context, runID, nodeID string, now time.Time) (ExecutorClaimRoundResult, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var readyAt, opened sql.NullInt64
	var claimedBy sql.NullString
	var status, requiredCoordinator, requiredLocation string
	err = tx.QueryRowContext(ctx, `SELECT ready_at, claimed_by, status, offer_started_at,
       required_coordinator_id, required_executor_location FROM nodes
	 WHERE run_id = ? AND node_id = ?`+tx.forUpdate(), runID, nodeID).Scan(
		&readyAt, &claimedBy, &status, &opened, &requiredCoordinator, &requiredLocation)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutorClaimRoundResult{}, ErrNotFound
	}
	if err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	if !readyAt.Valid || claimedBy.Valid || status == nodeStatusDone {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimRoundResult{}, err
		}
		return ExecutorClaimRoundResult{}, nil
	}
	if opened.Valid && now.Before(time.Unix(0, opened.Int64).Add(nodeClaimOfferWindow)) {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimRoundResult{}, err
		}
		return ExecutorClaimRoundResult{Pending: true}, nil
	}
	if winner, err := s.awardBestExecutorOffer(ctx, tx, runID, nodeID, now); err == nil && winner != nil {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimRoundResult{}, err
		}
		return ExecutorClaimRoundResult{}, nil
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return ExecutorClaimRoundResult{}, err
	}
	if requiredCoordinator != "" || requiredLocation != "" {
		if err := tx.Commit(); err != nil {
			return ExecutorClaimRoundResult{}, err
		}
		return ExecutorClaimRoundResult{Pending: true}, nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE nodes SET ready_at = NULL, offer_started_at = NULL
 WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL AND `+nodeNotDone, runID, nodeID)
	if err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_claim_offers WHERE run_id = ? AND node_id = ?`, runID, nodeID); err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExecutorClaimRoundResult{}, err
	}
	return ExecutorClaimRoundResult{Revoked: changed == 1}, nil
}
