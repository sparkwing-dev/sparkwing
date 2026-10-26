package store

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

const abandonedLogStreamSQL = `l.sealed_at IS NULL
AND EXISTS (SELECT 1 FROM runs r WHERE r.team=l.team AND r.id=l.run_id AND r.intake_mode='execute')
AND ((l.subject_kind='job_attempt'
    AND EXISTS (SELECT 1 FROM node_attempts a WHERE a.team=l.team AND a.run_id=l.run_id
        AND a.node_id=l.node_id AND a.attempt=l.attempt AND a.generation=l.generation AND a.finished_at<?))
OR (l.subject_kind='owner_unit' AND (
    (l.unit_kind IN ('trigger','plan') AND NOT EXISTS (
        SELECT 1 FROM triggers t WHERE t.team=l.team AND t.id=l.run_id AND t.unit_kind=l.unit_kind
        AND t.generation=l.generation AND ((t.status IN ('claimed','running') AND t.lease_expires_at>?) OR (t.finished_at>?))))
    OR (l.unit_kind='eval' AND NOT EXISTS (
        SELECT 1 FROM eval_units e JOIN nodes n ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id
        WHERE e.team=l.team AND e.run_id=l.run_id AND e.source_node_id=l.source_node_id
        AND e.source_generation=l.source_generation AND n.generation=l.source_generation
        AND e.generation=l.generation AND ((e.status IN ('claimed','running') AND e.lease_expires_at>?) OR (e.finished_at>?)))))))`

func abandonedLogStreamArgs(now time.Time) []any {
	return []any{now.Add(-limits.LogSealGrace).UnixNano(), now.UnixNano(), now.Add(-limits.LogSealGrace).UnixNano(), now.UnixNano(), now.Add(-limits.LogSealGrace).UnixNano()}
}

type abandonedLogStream struct {
	team    Team
	run, id string
}

// SealAbandonedLogStreams closes ended execution logs as incomplete under the
// maintenance fence. Completed attempts and owner units retain final-tail grace. Publication
// streams require their publisher's final commit because they have no lease.
func (o *Operator) SealAbandonedLogStreams(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, ErrInvalidInput
	}
	return inTxResult(ctx, o.s, func(tx *storeTx) (int64, error) {
		rows, err := tx.queryContext(ctx, `SELECT l.team,l.run_id,l.id FROM log_streams l WHERE `+abandonedLogStreamSQL+` ORDER BY l.team,l.run_id,l.id LIMIT ?`, append(abandonedLogStreamArgs(tx.now(ctx)), limit)...)
		if err != nil {
			return 0, err
		}
		var candidates []abandonedLogStream
		for rows.Next() {
			var candidate abandonedLogStream
			if err := rows.Scan(&candidate.team, &candidate.run, &candidate.id); err != nil {
				return 0, errors.Join(err, rows.Close())
			}
			candidates = append(candidates, candidate)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return 0, err
		}
		var sealed int64
		for _, candidate := range candidates {
			n, err := sealAbandonedLogStreamTx(ctx, tx, candidate)
			if err != nil {
				return 0, err
			}
			sealed += n
		}

		return sealed, nil
	})
}

func sealAbandonedLogStreamTx(ctx context.Context, tx *storeTx, stream abandonedLogStream) (int64, error) {
	if err := lockStateRunTx(ctx, tx, stream.team, stream.run); errors.Is(err, ErrNotFound) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	// safety: a run-lock wait can cross a writer's final commit.
	now := tx.now(ctx)
	args := append([]any{now.UnixNano(), now.UnixNano(), stream.team, stream.run, stream.id}, abandonedLogStreamArgs(now)...)
	result, err := tx.execContext(ctx, `UPDATE log_streams AS l SET sealed_at=?,updated_at=?,completeness='incomplete',incomplete_reason='writer_lost' WHERE l.team=? AND l.run_id=? AND l.id=? AND `+abandonedLogStreamSQL, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
