package store

import (
	"context"
	"database/sql"
	"errors"
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
}

// ReadRunView reads only this tenant's run, without exposing log storage locations.
func (t *Tenant) ReadRunView(ctx context.Context, runID string) (RunView, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return RunView{}, err
	}
	defer rollbackOrLog(tx)
	var view RunView
	if view.Snapshot, err = readSnapshotTx(ctx, tx, t.team, runID); err != nil {
		return RunView{}, err
	}
	if view.Attempts, err = readRunAttemptsTx(ctx, tx, t.team, runID); err != nil {
		return RunView{}, err
	}
	if view.Logs, err = readRunLogSubjectsTx(ctx, tx, t.team, runID); err != nil {
		return RunView{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunView{}, err
	}
	return view, nil
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
