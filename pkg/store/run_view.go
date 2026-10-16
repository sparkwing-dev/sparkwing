package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// RunView keeps status, attempt history and committed log identities in one snapshot.
// Snapshot contains private execution inputs; public adapters must project its seals.
type RunView struct {
	Snapshot RunSnapshot
	Attempts []NodeAttempt
	Logs     []blob.LogSubject
	Failures []string
}

// ReadRunView reads only this tenant's run, without exposing log storage locations.
func (t *Tenant) ReadRunView(ctx context.Context, runID string) (RunView, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return RunView{}, err
	}
	defer rollbackOrLog(tx)
	view, err := readRunViewTx(ctx, tx, t.team, runID)
	if err != nil {
		return RunView{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunView{}, err
	}
	return view, nil
}

func readRunViewTx(ctx context.Context, tx *storeTx, team Team, runID string) (RunView, error) {
	var view RunView
	var err error
	if view.Snapshot, err = readSnapshotTx(ctx, tx, team, runID); err != nil {
		return RunView{}, err
	}
	if view.Failures, err = readRunFailuresTx(ctx, tx, team, runID); err != nil {
		return RunView{}, err
	}
	if view.Attempts, err = readRunAttemptsTx(ctx, tx, team, runID); err != nil {
		return RunView{}, err
	}
	if view.Logs, err = readRunLogSubjectsTx(ctx, tx, team, runID); err != nil {
		return RunView{}, err
	}
	return view, nil
}

func readRunFailuresTx(ctx context.Context, tx *storeTx, team Team, runID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT error FROM runs WHERE team=? AND id=? AND error<>'' UNION ALL SELECT error FROM nodes WHERE team=? AND run_id=? AND error<>''`, team, runID, team, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	failures := []string{}
	for rows.Next() {
		var failure string
		if err := rows.Scan(&failure); err != nil {
			return nil, err
		}
		failures = append(failures, failure)
	}
	return failures, errors.Join(rows.Err(), rows.Close())
}

// ListRunViews reads a bounded tenant page in descending number order within exclusive lower and inclusive upper bounds.
func (t *Tenant) ListRunViews(ctx context.Context, after, through int64, limit int) ([]RunView, error) {
	if after < 0 || through < after || limit < 1 {
		return nil, fmt.Errorf("run page requires ordered nonnegative bounds and a positive limit")
	}
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM runs WHERE team=? AND number>? AND number<=? ORDER BY number DESC LIMIT ?`, t.team, after, through, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	views := make([]RunView, 0, len(ids))
	for _, id := range ids {
		view, err := readRunViewTx(ctx, tx, t.team, id)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, tx.Commit()
}

func readRunAttemptsTx(ctx context.Context, tx *storeTx, team Team, runID string) ([]NodeAttempt, error) {
	rows, err := tx.QueryContext(ctx, `SELECT node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome,failure_reason FROM node_attempts WHERE team=? AND run_id=? ORDER BY node_id,attempt`, team, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := []NodeAttempt{}
	for rows.Next() {
		attempt := NodeAttempt{Identity: runspec.AttemptIdentity{Team: string(team), RunID: runID}}
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&attempt.Identity.NodeID, &attempt.Identity.Attempt, &attempt.Identity.Generation, &attempt.Holder, &attempt.CapabilityPrefix, &attempt.ExecutorKind, &attempt.ExecutorName, &attempt.ExecutorLocation, &started, &finished, &attempt.Outcome, &attempt.FailureReason); err != nil {
			return nil, err
		}
		attempt.StartedAt = time.Unix(0, started).UTC()
		attempt.FinishedAt = stateTime(finished)
		attempts = append(attempts, attempt)
	}
	return attempts, errors.Join(rows.Err(), rows.Close())
}

func readRunLogSubjectsTx(ctx context.Context, tx *storeTx, team Team, runID string) ([]blob.LogSubject, error) {
	rows, err := tx.QueryContext(ctx, `SELECT subject_kind,node_id,attempt,generation,unit_kind,source_node_id,source_generation FROM log_streams WHERE team=? AND run_id=? ORDER BY created_at,id`, team, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	subjects := []blob.LogSubject{}
	for rows.Next() {
		subject := blob.LogSubject{Team: string(team), RunID: runID}
		var node, kind, source sql.NullString
		var attempt, sourceGeneration sql.NullInt64
		var generation int64
		if err := rows.Scan(&subject.Kind, &node, &attempt, &generation, &kind, &source, &sourceGeneration); err != nil {
			return nil, err
		}
		switch subject.Kind {
		case blob.LogJobAttempt:
			subject.Attempt = &runspec.AttemptIdentity{Team: string(team), RunID: runID, NodeID: node.String, Attempt: attempt.Int64, Generation: generation}
		case blob.LogOwnerUnit:
			subject.Unit = &blob.LogUnit{Kind: blob.LogUnitKind(kind.String), Generation: generation}
			if subject.Unit.Kind == blob.LogEval {
				subject.Unit.Source = &blob.LogSource{NodeID: source.String, Generation: sourceGeneration.Int64}
			}
		default:
			return nil, authorityError(apierr.IntegrityFailure, "invalid persisted log subject")
		}
		subjects = append(subjects, subject)
	}
	return subjects, errors.Join(rows.Err(), rows.Close())
}
