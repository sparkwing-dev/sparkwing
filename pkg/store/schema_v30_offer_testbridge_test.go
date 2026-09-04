package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// TestOnlyOfferExecutorClaim preserves the schema30 arbitration causals after
// schema31 closes production offers over unsealed nodes.
func (s *Store) TestOnlyOfferExecutorClaim(ctx context.Context, claimant ClaimIdentity, offer ExecutorClaimOffer) (ExecutorClaimOfferResult, error) {
	return s.recordExecutorOfferAt(ctx, claimant, offer, time.Now())
}

// TestOnlyPrepareNextExecutorClaim preserves the schema30 preparation causals
// without reopening unsealed selection in production.
func (s *Store) TestOnlyPrepareNextExecutorClaim(ctx context.Context, claimant ClaimIdentity, executorName string) (*ExecutorClaimPreparation, error) {
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
	   AND (? = '' OR n.run_id = ?)
	   AND NOT EXISTS (
	       SELECT 1 FROM node_claim_offers o
	        WHERE o.run_id = n.run_id AND o.node_id = n.node_id
	          AND o.executor_name = ? AND o.last_seen_at >= ?)
   AND (n.ready_at > ? OR (n.ready_at = ? AND (n.run_id > ? OR (n.run_id = ? AND n.node_id > ?))))
 ORDER BY n.ready_at, n.run_id, n.node_id
	LIMIT 64`, "", "", executorName, activeAfter, after.readyAt, after.readyAt, after.runID, after.runID, after.nodeID)
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
			membership, err := s.resolveExecutorMembership(ctx, claimant, executorName, summary, time.Now())
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
				node.AvoidCoordinatorID == coordinatorID && node.AvoidExecutorKind == executor.Kind && node.AvoidExecutorID == executor.id {
				alternate, err := s.hasAlternateEligibleExecutor(ctx, executor.Name, summary)
				if err != nil {
					return nil, err
				}
				if alternate {
					continue
				}
			}
			var opened sql.NullInt64
			if err := s.queryRow(ctx, `SELECT offer_started_at, offer_priority_target FROM nodes WHERE run_id = ? AND node_id = ?`, item.runID, item.nodeID).Scan(&opened, &membership.HighestEligiblePriority); err != nil {
				return nil, err
			}
			preparation := &ExecutorClaimPreparation{Summary: summary, Membership: membership}
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
