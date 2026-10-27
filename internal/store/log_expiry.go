package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

type expiredLogObject struct {
	team, run, stream, key string
	index                  int64
}

// DeleteExpiredLogObjects deletes log objects whose slot was first signed longer ago than its stream's retention
// class, then stops counting each one. Sparkwing is the deleter: a bucket lifecycle rule is only a later backstop,
// so retained bytes never fall on an inferred deletion. A failed delete keeps its slot counted for the next pass.
func (o *Operator) DeleteExpiredLogObjects(ctx context.Context, limit int, objects blob.LogObjectDeleter) (int, error) {
	if limit <= 0 || objects == nil {
		return 0, ErrInvalidInput
	}
	var expired []expiredLogObject
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		expired = nil
		rows, err := tx.queryContext(ctx, `SELECT DISTINCT retention_class FROM log_streams s WHERE EXISTS(SELECT 1 FROM log_slots l WHERE l.team=s.team AND l.run_id=s.run_id AND l.stream_id=s.id) ORDER BY retention_class`)
		if err != nil {
			return err
		}
		var classes []string
		for rows.Next() {
			var class string
			if err := rows.Scan(&class); err != nil {
				return errors.Join(err, rows.Close())
			}
			classes = append(classes, class)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, class := range classes {
			days, ok := blob.LogClassDays(class)
			if !ok {
				return fmt.Errorf("storage: invalid persisted log retention class %q", class)
			}
			cutoff, err := retentionBoundary(tx.now(ctx), int64(days))
			if err != nil {
				return err
			}
			rows, err := tx.queryContext(ctx, `SELECT l.team,l.run_id,l.stream_id,l.n,l.object_key FROM log_slots l JOIN log_streams s ON s.team=l.team AND s.run_id=l.run_id AND s.id=l.stream_id
				WHERE s.retention_class=? AND l.created_at<=? ORDER BY l.created_at,l.team,l.run_id,l.stream_id,l.n LIMIT ?`, class, cutoff.UnixNano(), limit-len(expired))
			if err != nil {
				return err
			}
			for rows.Next() {
				var e expiredLogObject
				if err := rows.Scan(&e.team, &e.run, &e.stream, &e.index, &e.key); err != nil {
					return errors.Join(err, rows.Close())
				}
				expired = append(expired, e)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil || len(expired) >= limit {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var failed error
	deleted := 0
	for _, e := range expired {
		if err := objects.DeleteLogObject(ctx, e.team, e.run, e.key); err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		err := o.s.inTx(ctx, func(tx *storeTx) error {
			_, err := tx.execContext(ctx, `DELETE FROM log_slots WHERE team=? AND run_id=? AND stream_id=? AND n=? AND object_key=?`, e.team, e.run, e.stream, e.index, e.key)
			return err
		})
		if err != nil {
			return deleted, errors.Join(failed, err)
		}
		deleted++
	}
	return deleted, failed
}
