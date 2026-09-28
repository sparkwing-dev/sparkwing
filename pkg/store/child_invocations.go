package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// EnqueueChildRun starts the child run one RunAndAwait call names, keyed by
// its invocation ID: the claim's run, node and generation plus ordinal, the
// call's position within the attempt. t becomes the child's trigger, with
// its parent taken from tok, never from t.
//
// The same invocation ID returns the same child, so a retry after a lost
// response starts nothing, while two calls with different ordinals start two
// children; the same ID with a different pipeline or arguments is
// [ErrClaimResultConflict]. A later attempt at the node reuses an earlier
// attempt's child at the same ordinal, pipeline and arguments only while that
// child is provably queued, running or succeeded. The claim must be live with
// no cancel requested on its run.
func (s *Store) EnqueueChildRun(ctx context.Context, tok ClaimToken, ordinal int64, t Trigger, now time.Time) (childID string, err error) {
	if ordinal < 0 || t.Pipeline == "" || t.ID == "" {
		return "", fmt.Errorf("%w: a child run needs an ID, a pipeline and a non-negative ordinal", ErrInvalidInput)
	}
	request, err := childRequestDigest(t)
	if err != nil {
		return "", err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockDispatchRunTx(ctx, tx, tok.Team, tok.RunID); err != nil {
		return "", err
	}
	live, err := claimLiveTx(ctx, tx, tok.Team, tok.RunID, tok.NodeID, tok.Generation, now)
	if err != nil {
		return "", err
	}
	if !live {
		return "", ErrClaimNotLive
	}
	cancelled, err := claimRunCancelled(ctx, tx.QueryRowContext, tok.Team, tok.RunID)
	if err != nil {
		return "", err
	}
	if cancelled {
		return "", ErrClaimCancelRequested
	}
	var committed string
	err = tx.QueryRowContext(ctx, `SELECT child_run_id, request_digest FROM child_invocations
 WHERE team = ? AND parent_run_id = ? AND parent_node_id = ? AND claim_generation = ? AND ordinal = ?`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation, ordinal).Scan(&childID, &committed)
	switch {
	case err == nil && committed != request:
		return "", fmt.Errorf("%w: invocation %d already started a child with other inputs", ErrClaimResultConflict, ordinal)
	case err == nil:
		return childID, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	// safety: a child is reused only on evidence it will still succeed: a run
	// that succeeded, or one queued or running with no cancel request on it or
	// its trigger. A trigger no run row backs counts only while it is queued or
	// claimed, so one finished or cancelled before dispatch starts a new child.
	err = tx.QueryRowContext(ctx, `SELECT c.child_run_id FROM child_invocations c
  JOIN triggers t ON t.team = c.team AND t.id = c.child_run_id
  LEFT JOIN runs r ON r.team = c.team AND r.id = c.child_run_id
 WHERE c.team = ? AND c.parent_run_id = ? AND c.parent_node_id = ? AND c.ordinal = ? AND c.request_digest = ?
   AND c.claim_generation < ?
   AND (r.status = ? OR (t.cancel_requested_at IS NULL AND (
        (r.id IS NULL AND t.status IN (?, ?)) OR (r.status IN (?, ?) AND r.cancel_requested_at IS NULL))))
 ORDER BY c.claim_generation DESC LIMIT 1`,
		string(tok.Team), tok.RunID, tok.NodeID, ordinal, request, tok.Generation,
		runStatusSuccess, triggerStatusPending, triggerStatusClaimed, runStatusPending, runStatusRunning).Scan(&childID)
	if errors.Is(err, sql.ErrNoRows) {
		t.ParentRunID, t.ParentNodeID, t.Status = tok.RunID, tok.NodeID, ""
		if t.CreatedAt.IsZero() {
			t.CreatedAt = now
		}
		// safety: only a new child is admitted, so a replay at the team's
		// daily cap still answers; admission's free-tier lock follows the run
		// and node locks above, in the order lockTeamRunRowTx names.
		if err := createTriggerTx(ctx, tx, tok.Team, t); err != nil {
			return "", err
		}
		childID, err = t.ID, nil
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO child_invocations
       (team, parent_run_id, parent_node_id, claim_generation, ordinal, request_digest, child_run_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation, ordinal, request, childID, now.UnixNano()); err != nil {
		return "", err
	}
	return childID, tx.Commit()
}

// safety: encoding/json writes map keys sorted, so equal arguments always
// digest the same.
func childRequestDigest(t Trigger) (string, error) {
	raw, err := json.Marshal(struct {
		Pipeline string            `json:"pipeline"`
		Args     map[string]string `json:"args"`
	}{t.Pipeline, t.Args})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
