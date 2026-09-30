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
	// safety: a child is reused only on evidence it will still succeed: a run that succeeded, or one queued or running
	// with no cancel request on it or its trigger. A trigger-path child also needs its trigger queued or claimed, so one
	// that ended before its run starts a new child; a controller-dispatched child's trigger is done from its intake.
	err = tx.QueryRowContext(ctx, `SELECT c.child_run_id FROM child_invocations c
  JOIN triggers t ON t.team = c.team AND t.id = c.child_run_id
  JOIN runs r ON r.team = c.team AND r.id = c.child_run_id
 WHERE c.team = ? AND c.parent_run_id = ? AND c.parent_node_id = ? AND c.ordinal = ? AND c.request_digest = ?
   AND c.claim_generation < ?
   AND (r.status = ? OR (t.cancel_requested_at IS NULL AND r.cancel_requested_at IS NULL AND r.status IN (?, ?)
        AND (r.dispatch != '' OR t.status IN (?, ?))))
 ORDER BY c.claim_generation DESC LIMIT 1`,
		string(tok.Team), tok.RunID, tok.NodeID, ordinal, request, tok.Generation,
		runStatusSuccess, runStatusPending, runStatusRunning, triggerStatusPending, triggerStatusClaimed).Scan(&childID)
	if errors.Is(err, sql.ErrNoRows) {
		t.ParentRunID, t.ParentNodeID, t.Status = tok.RunID, tok.NodeID, ""
		if t.CreatedAt.IsZero() {
			t.CreatedAt = now
		}
		if err := inheritRepoIDTx(ctx, tx, tok, &t); err != nil {
			return "", err
		}
		// safety: only a new child is admitted, so a replay at the team's
		// daily cap still answers; admission's free-tier lock follows the run
		// and node locks above, in the order lockTeamRunRowTx names.
		if err := createTriggerTx(ctx, tx, tok.Team, t); err != nil {
			return "", err
		}
		if err := s.createRunTx(ctx, tx, tok.Team, childRun(t)); err != nil {
			return "", err
		}
		// safety: a child of an opted-in repository is planned by the
		// controller like any other run of it, never claimed as a trigger.
		if err := routeRunDispatchTx(ctx, tx, tok.Team, t, now); err != nil {
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

// safety: a Cloud source credential reads a repository by its GitHub ID, so a
// child of its parent's repository carries the ID its parent's trigger holds.
func inheritRepoIDTx(ctx context.Context, tx *storeTx, tok ClaimToken, t *Trigger) error {
	var id int64
	var owner, repo string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(github_repo_id, 0), github_owner, github_repo FROM triggers WHERE team = ? AND id = ?`,
		string(tok.Team), tok.RunID).Scan(&id, &owner, &repo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err == nil && t.GithubRepoID == 0 && RepoKey(owner, repo) == RepoKey(t.GithubOwner, t.GithubRepo) {
		t.GithubRepoID = id
	}
	return err
}

func childRun(t Trigger) Run {
	return Run{
		ID: t.ID, Pipeline: t.Pipeline, Status: runStatusPending, TriggerSource: t.TriggerSource,
		GitBranch: t.GitBranch, GitSHA: t.GitSHA, Args: t.Args, ParentRunID: t.ParentRunID,
		DeclaredRepo: t.Repo, RepoURL: t.RepoURL, GithubOwner: t.GithubOwner, GithubRepo: t.GithubRepo,
		CreatedAt: t.CreatedAt, StartedAt: t.CreatedAt,
	}
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

// IsChildRunOf reports whether childID is a run that parentNodeID of
// parentRunID started through [Store.EnqueueChildRun].
func (s *Store) IsChildRunOf(ctx context.Context, team Team, parentRunID, parentNodeID, childID string) (bool, error) {
	var one int
	err := s.queryRow(ctx, `SELECT 1 FROM child_invocations
 WHERE team = ? AND parent_run_id = ? AND parent_node_id = ? AND child_run_id = ? LIMIT 1`,
		string(team), parentRunID, parentNodeID, childID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
