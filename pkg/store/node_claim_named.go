package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// NamedClaimOptions says what a named claim will give the node it takes.
type NamedClaimOptions struct {
	// SizesToClass reports that the caller creates the node's executor at the
	// cpu class the claim bills, which is what admits a class above the warm
	// pool. A caller that runs the node on a pod it already has leaves it
	// false and is held to the warm class.
	SizesToClass bool
}

// ClaimNamedNode awards one node the caller names to holderID with a fresh
// lease, through the award and credit reservation [Store.ClaimNextReadyNode]
// uses. It is the claim a dispatcher makes for a node it is about to execute
// itself, so its own process holds the fence every node mutation is checked
// against.
//
// Unlike the queue claim it passes over no candidate and reads no label: the
// caller already decided this node is its work. It still refuses a node
// another claim holds, a node that finished, a node of a run that finished,
// one pinned to a coordinator or executor location, and one whose execution
// policy is sealed. A node that exists but cannot be awarded returns
// [ErrLockHeld]; a node that does not exist returns [ErrNotFound].
//
// It reads no readiness flag, because the coordinator fallback it serves takes
// the node in the same breath as the controller withdraws it from the queue.
// Deciding who may name a node is the caller's gate, not this one.
//
// claimant is the authenticated token the claim answers to, exactly as for
// [Store.ClaimNextReadyNode]: a metered token reserves credits here, and
// [Store.HeartbeatNodeClaim] admits only that token afterwards. lease is
// clamped to [MaxLeaseDuration].
//
// opts says what the caller will give the node. A metered caller that does not
// size its executor to the node's cpu class is refused a class larger than the
// warm pool serves, exactly as the queue claim passes over one, so naming a
// node is not a way around the ladder.
func (s *Store) ClaimNamedNode(
	ctx context.Context, claimant ClaimIdentity, runID, nodeID, holderID string,
	lease time.Duration, opts NamedClaimOptions,
) (*Node, error) {
	lease = clampNodeLease(lease)
	if !opts.SizesToClass {
		warm, err := s.warmClassFilter(ctx, claimant)
		if err != nil {
			return nil, err
		}
		refused, err := warm.refuses(ctx, storeRowQuerier{s}, runID, nodeID)
		if err != nil {
			return nil, err
		}
		if refused {
			return nil, ErrLockHeld
		}
	}
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return nil, err
	}
	var status, runStatus string
	var claimedBy sql.NullString
	var needsJSON, prefersJSON []byte
	err = s.queryRow(ctx, `SELECT n.status, n.claimed_by, n.needs_labels, n.prefers_labels, r.status
 FROM nodes n JOIN runs r ON r.id = n.run_id
	WHERE n.run_id = ? AND n.node_id = ?`, runID, nodeID).Scan(
		&status, &claimedBy, &needsJSON, &prefersJSON, &runStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("node", runID+"/"+nodeID)
	}
	if err != nil {
		return nil, err
	}
	if status == nodeStatusDone || claimedBy.Valid || isTerminalRunStatus(runStatus) {
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
