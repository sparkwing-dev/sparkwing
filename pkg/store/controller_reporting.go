package store

import (
	"context"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// PrepareLogWrite checks the exact current writer and returns the run's sealed retention class.
func (t *Tenant) PrepareLogWrite(ctx context.Context, subject blob.LogSubject, actor Actor) (string, error) {
	if subject.Team != string(t.team) {
		return "", authorityError(apierr.Forbidden, "log belongs to another team")
	}
	if err := subject.Validate(); err != nil {
		return "", authorityError(apierr.BadRequest, "invalid log subject")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackOrLog(tx)
	if err := lockStateRunTx(ctx, tx, t.team, subject.RunID); err != nil {
		return "", err
	}
	if err := checkLogWriterTx(ctx, tx, t.team, subject, actor, false); err != nil {
		return "", err
	}
	var class string
	if err := tx.QueryRowContext(ctx, `SELECT log_retention_class FROM runs WHERE team=? AND id=?`, t.team, subject.RunID).Scan(&class); err != nil {
		return "", err
	}
	if _, ok := blob.LogClassDays(class); !ok {
		return "", authorityError(apierr.Unavailable, "invalid persisted log retention")
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return class, nil
}

// WriteActivity records bounded masked detail under the current result writer's fence.
func (t *Tenant) WriteActivity(ctx context.Context, ref NodeRef, actor Actor, detail string) error {
	if err := boundedResultText(detail); err != nil {
		return err
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if _, err := checkResultWriterTx(ctx, tx, t.team, ref, actor); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET status_detail=? WHERE team=? AND run_id=? AND node_id=?`, detail, t.team, ref.RunID, ref.NodeID); err != nil {
		return err
	}
	return tx.Commit()
}
