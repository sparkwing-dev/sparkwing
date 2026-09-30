package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Attempt report limits: a larger output is refused, a longer error is cut.
const (
	MaxAttemptOutputBytes = 1 << 20
	maxAttemptErrorBytes  = 16 << 10
)

// ErrAttemptInvalid refuses an attempt report the store will not record; the
// wrapped message names the reason.
var ErrAttemptInvalid = errors.New("store: attempt report refused")

// AttemptReport is what a claim reports when its attempt at a node ends.
type AttemptReport struct {
	// Outcome is the node outcome the attempt reached, one of the SDK's
	// terminal outcomes. A planning claim reports only failed or cancelled;
	// its success is [Store.AcceptPlan].
	Outcome       string `json:"outcome"`
	Error         string `json:"error,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
	// Output is the node's JSON output, stored for its dependents.
	Output json.RawMessage `json:"output,omitempty"`
	// Failure is the failure record an OnFailure recovery node receives.
	Failure          json.RawMessage `json:"failure,omitempty"`
	ArtifactManifest string          `json:"artifact_manifest,omitempty"`

	// safety: set only by the expired-claim reaper, never decoded from a
	// request, so a claimant cannot pass its own failure off as a lost claim.
	leaseLost bool
}

func attemptRefused(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrAttemptInvalid, fmt.Sprintf(format, args...))
}

func (r *AttemptReport) validate(kind ClaimTokenKind) error {
	switch r.Outcome {
	case outcomeFailed, outcomeCancelled:
	case outcomeSuccess, outcomeSatisfied, outcomeCached, outcomeSkipped, outcomeSkippedConcurrent, outcomeSuperseded:
		if kind == ClaimTokenPlan {
			return attemptRefused("a planning claim reports success by submitting its plan")
		}
	default:
		return attemptRefused("outcome %q is not a node outcome", r.Outcome)
	}
	if len(r.Output) > MaxAttemptOutputBytes {
		return attemptRefused("output is %d bytes; the limit is %d", len(r.Output), MaxAttemptOutputBytes)
	}
	for name, raw := range map[string]json.RawMessage{"output": r.Output, "failure": r.Failure} {
		if len(raw) > 0 && !json.Valid(raw) {
			return attemptRefused("%s is not JSON", name)
		}
	}
	if len(r.Error) > maxAttemptErrorBytes {
		r.Error = r.Error[:maxAttemptErrorBytes]
	}
	return nil
}

// ReportAttempt commits the end of the claim's attempt at its node as the
// claim's one result. One transaction under the run row records the attempt,
// decides a retry within the node's budget or writes the node's final outcome
// and output, ends the claim, settles the run and bills the attempt. A failed
// attempt the node will retry leaves the node pending with a later ready
// time, so no dependent is released between attempts.
//
// It reports replayed when the claim already committed this exact report,
// and writes nothing then.
func (s *Store) ReportAttempt(ctx context.Context, commit ClaimResultCommit, report AttemptReport, now time.Time) (replayed bool, err error) {
	tok := commit.Token()
	if err := report.validate(tok.Kind); err != nil {
		return false, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockDispatchRunTx(ctx, tx, tok.Team, tok.RunID); err != nil {
		return false, err
	}
	if replayed, err := commit.commitTx(ctx, tx, now); err != nil || replayed {
		return replayed, err
	}
	if err := s.commitAttemptTx(ctx, tx, tok, report, now); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

type attemptNode struct {
	kind, lineageRoot, tokenPrefix, holder, membership, executor, executorKind string
	location, reservation                                                      string
	consumed, retryBudget, backoffMS, chargedThrough                           int64
	startedAt                                                                  sql.NullInt64
}

// safety: the caller holds the run row and has committed tok's result digest,
// so this runs once per claim and every write below lands with that digest or
// not at all.
func (s *Store) commitAttemptTx(ctx context.Context, tx *storeTx, tok ClaimToken, report AttemptReport, now time.Time) error {
	return s.commitAttemptBilledTx(ctx, tx, tok, report, now, now)
}

// safety: billedTo ends the attempt's charge window, which for a claim whose
// lease lapsed is the lapse, not the later pass that noticed it.
func (s *Store) commitAttemptBilledTx(ctx context.Context, tx *storeTx, tok ClaimToken, report AttemptReport,
	now, billedTo time.Time,
) error {
	var n attemptNode
	err := tx.QueryRowContext(ctx, `SELECT kind, retry_root_run_id, claim_token_prefix, COALESCE(claimed_by, ''),
       claim_membership_id, claim_executor, claim_executor_kind, executor_location, reservation_id,
       attempts_consumed, retry_budget, retry_backoff_ms, credit_charged_through, execution_started_at
  FROM nodes WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ?`+tx.forUpdate(),
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation).Scan(
		&n.kind, &n.lineageRoot, &n.tokenPrefix, &n.holder, &n.membership, &n.executor, &n.executorKind,
		&n.location, &n.reservation, &n.consumed, &n.retryBudget, &n.backoffMS, &n.chargedThrough, &n.startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClaimNotLive
	}
	if err != nil {
		return err
	}
	if n.kind != string(tok.Kind) {
		return fmt.Errorf("%w: a %s claim cannot report for a %q node", ErrClaimResultConflict, tok.Kind, n.kind)
	}
	if err := insertAttemptRowTx(ctx, tx, tok, n, report, now); err != nil {
		return err
	}
	ordinal := n.consumed + 1
	// safety: a run with a cancel request retries nothing, so a node failing
	// while its run is cancelled stays failed instead of being claimed again.
	cancelled, err := claimRunCancelled(ctx, tx.QueryRowContext, tok.Team, tok.RunID)
	if err != nil {
		return err
	}
	// safety: a planning claim's own failure is deterministic, so only a
	// planning claim the reaper found lost, or whose source fetch failed, is
	// planned again.
	retry := report.Outcome == outcomeFailed && ordinal <= n.retryBudget && !cancelled &&
		report.FailureReason != FailureSourceUnavailable &&
		(n.kind != nodeKindPlan || report.leaseLost || report.FailureReason == FailureSourceFetch)
	if retry {
		readyAt := now.Add(retryBackoff(time.Duration(n.backoffMS)*time.Millisecond, int(ordinal))).UnixNano()
		_, err = tx.ExecContext(ctx, `UPDATE nodes
   SET status = ?, outcome = '', error = ?, failure_reason = ?, output_json = NULL, failure_json = NULL,
       started_at = NULL, finished_at = NULL, attempts_consumed = ?,
       ready_at = ?, placement_hold_from = ?, offer_started_at = NULL,
       `+endClaimSet+`
 WHERE team = ? AND run_id = ? AND node_id = ?`,
			nodeStatusPending, report.Error, report.FailureReason, ordinal, readyAt, readyAt,
			string(tok.Team), tok.RunID, tok.NodeID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE nodes
   SET status = ?, outcome = ?, error = ?, failure_reason = ?, output_json = ?, failure_json = ?,
       artifact_manifest = ?, finished_at = ?, attempts_consumed = ?,
       `+endClaimSet+`
 WHERE team = ? AND run_id = ? AND node_id = ?`,
			nodeStatusDone, report.Outcome, report.Error, report.FailureReason, nullableJSON(report.Output),
			nullableJSON(report.Failure), report.ArtifactManifest, now.UnixNano(), ordinal,
			string(tok.Team), tok.RunID, tok.NodeID)
	}
	if err != nil {
		return err
	}
	if err := settleTx(ctx, tx, tok.Team, tok.RunID, now); err != nil {
		return err
	}
	// safety: the ledger lock is global and last in the store's lock order, so
	// the charge follows settle's node writes, and an unmetered attempt, whose
	// charge window never opened, skips it.
	if n.chargedThrough != 0 {
		if _, err := s.chargeNodeTx(ctx, tx, tok.RunID, tok.NodeID, n.tokenPrefix, billedTo, true); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE nodes SET credit_charged_through = 0,
       execution_started_at = CASE WHEN status = ? THEN NULL ELSE execution_started_at END
 WHERE team = ? AND run_id = ? AND node_id = ?`, nodeStatusPending, string(tok.Team), tok.RunID, tok.NodeID)
	return err
}

// safety: claim_generation is kept, so the next claim takes a later generation
// and every token of this one stays ended.
const endClaimSet = `claimed_by = NULL, claim_principal = '', claim_token_prefix = '',
       claim_executor = '', claim_cores = 0, claim_memory_bytes = 0, claim_reservation = '',
       claim_slot = -1, lease_expires_at = NULL, reservation_id = '', claim_membership_id = '',
       claim_worker_id = '', claim_executor_kind = '', claim_reservation_id = '', last_heartbeat = NULL`

func insertAttemptRowTx(ctx context.Context, tx *storeTx, tok ClaimToken, n attemptNode, report AttemptReport, now time.Time) error {
	coordinatorID, err := coordinatorIDTx(ctx, tx)
	if err != nil {
		return err
	}
	root := n.lineageRoot
	if root == "" {
		root = tok.RunID
	}
	started := now.UnixNano()
	if n.startedAt.Valid {
		started = n.startedAt.Int64
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO node_execution_attempts
    (team, lineage_root_run_id, run_id, node_id, attempt_ordinal, claim_generation,
     coordinator_id, membership_id, executor_kind, executor_name, executor_id, executor_location,
     holder_id, reservation_id, started_at, finished_at, outcome, failure_reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(tok.Team), root, tok.RunID, tok.NodeID, n.consumed+1, tok.Generation,
		coordinatorID, n.membership, n.executorKind, n.executor, n.holder, n.location,
		n.holder, n.reservation, started, now.UnixNano(), report.Outcome, report.FailureReason)
	return err
}

func nullableJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func retryBackoff(initial time.Duration, attempt int) time.Duration {
	if initial <= 0 || attempt <= 0 {
		return 0
	}
	out := initial
	for i := 1; i < attempt; i++ {
		out *= 2
		if out >= MaxRetryBackoff {
			return MaxRetryBackoff
		}
	}
	return out
}
