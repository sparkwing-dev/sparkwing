package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ClaimNamedNode awards one node the caller names to holderID with a fresh
// lease, through the award and credit reservation [Store.ClaimNextReadyNode]
// uses. It is the claim a dispatcher makes for a node it is about to execute
// itself, so its own process holds the fence every node mutation is checked
// against.
//
// Unlike the queue claim it passes over no candidate and reads no label: the
// caller already decided this node is its work. It still refuses a node
// another claim holds, a node that finished, one pinned to a different
// coordinator or executor location, and one whose execution policy is sealed.
// A node that exists but cannot be awarded returns [ErrLockHeld]; a node that
// does not exist returns [ErrNotFound].
//
// claimant is the authenticated token the claim answers to, exactly as for
// [Store.ClaimNextReadyNode]: a metered token reserves credits here, and
// [Store.HeartbeatNodeClaim] admits only that token afterwards. lease is
// clamped to [MaxLeaseDuration].
func (s *Store) ClaimNamedNode(ctx context.Context, claimant ClaimIdentity, runID, nodeID, holderID string, lease time.Duration) (*Node, error) {
	lease = clampNodeLease(lease)
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return nil, err
	}
	var status string
	var claimedBy sql.NullString
	var needsJSON, prefersJSON []byte
	err = s.queryRow(ctx, `SELECT status, claimed_by, needs_labels, prefers_labels
 FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID).Scan(
		&status, &claimedBy, &needsJSON, &prefersJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("node", runID+"/"+nodeID)
	}
	if err != nil {
		return nil, err
	}
	if status == nodeStatusDone || claimedBy.Valid {
		return nil, ErrLockHeld
	}
	candidate := claimCandidate{runID: runID, nodeID: nodeID}
	decodeCandidateLabels(runID, nodeID, needsJSON, &candidate.needs)
	decodeCandidateLabels(runID, nodeID, prefersJSON, &candidate.prefers)
	placement, _ := ClaimPlacementFromContext(ctx)
	// safety: naming a node is the dispatcher taking work no preferred runner
	// took, which is what the fallback reason records.
	candidate.decision = placementDecision{
		reason: PlacementFallback, nodeOwned: len(candidate.prefers) > 0,
	}
	if len(placement.prefersFor(candidate.prefers)) == 0 {
		candidate.decision = placementDecision{reason: PlacementNone}
	}
	n, err := s.awardScannedNode(ctx, candidate, claimant, holderID, coordinatorID, lease, placement, false)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, ErrLockHeld
	}
	return n, nil
}
