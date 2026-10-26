package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// PrepareLogWrite checks the exact current writer and returns the run's sealed retention class.
func (t *Tenant) PrepareLogWrite(ctx context.Context, subject blob.LogSubject, actor runstate.Actor) (string, error) {
	if subject.Team != string(t.team) {
		return "", authorityError(apierr.Forbidden, "log belongs to another team")
	}
	if err := subject.Validate(); err != nil {
		return "", authorityError(apierr.BadRequest, "invalid log subject")
	}
	var class string
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		class, err = t.prepareLogWriteTx(ctx, tx, subject, actor)
		return err
	})
	if err != nil {
		return "", err
	}
	return class, nil
}

func (t *Tenant) prepareLogWriteTx(ctx context.Context, tx *storeTx, subject blob.LogSubject, actor runstate.Actor) (string, error) {
	var class string
	if err := lockAuthorityThenRunTx(ctx, tx, t.team, subject.RunID, actor); err != nil {
		return "", err
	}
	if err := checkLogWriterTx(ctx, tx, t.team, subject, actor, false); err != nil {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT log_retention_class FROM runs WHERE team=? AND id=?`, t.team, subject.RunID).Scan(&class); err != nil {
		return "", err
	}
	if _, ok := blob.LogClassDays(class); !ok {
		return "", authorityError(apierr.Unavailable, "invalid persisted log retention")
	}
	return class, nil
}

// ReserveLogUpload checks the writer as PrepareLogWrite does and admits the slot's bytes through the storage rule
// before the caller signs an upload URL, so a refused team gets no URL. It returns the retention class and when the
// slot's URL and hold expire; the segment commit, or the final commit for the manifest at index 0, settles the hold.
// Signing a slot again reuses its live hold.
func (t *Tenant) ReserveLogUpload(ctx context.Context, subject blob.LogSubject, actor runstate.Actor, index, size int64) (string, time.Time, error) {
	if subject.Team != string(t.team) {
		return "", time.Time{}, authorityError(apierr.Forbidden, "log belongs to another team")
	}
	if err := subject.Validate(); err != nil {
		return "", time.Time{}, authorityError(apierr.BadRequest, "invalid log subject")
	}
	identity, err := logIdentity(subject)
	if err != nil {
		return "", time.Time{}, err
	}
	if index < 0 || size < 1 {
		return "", time.Time{}, authorityError(apierr.BadRequest, "invalid log upload slot")
	}
	var class string
	var expires time.Time
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		if class, err = t.prepareLogWriteTx(ctx, tx, subject, actor); err != nil {
			return err
		}
		expires = tx.now(ctx).Add(limits.LogUploadURLTTL)
		id := logReservationID(identity.id, index)
		var held bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quota_reservations WHERE team=? AND id=? AND state='held' AND expires_at>?)`, t.team, id, tx.now(ctx).UnixNano()).Scan(&held); err != nil || held {
			return err
		}
		return admitStorageGrowthTx(ctx, tx, t.team, storageGrowth{reservation: id, expires: expires, run: subject.RunID, bytes: size})
	})
	return class, expires, err
}

// WriteActivity records bounded masked detail under the current result writer's fence.
func (t *Tenant) WriteActivity(ctx context.Context, ref runspec.NodeRef, actor runstate.Actor, detail string) error {
	if err := boundedResultText(detail); err != nil {
		return err
	}
	return t.s.inTxOnce(ctx, func(tx *storeTx) error {
		if _, err := checkResultWriterTx(ctx, tx, t.team, ref, actor); err != nil {
			return err
		}
		if _, err := tx.execContext(ctx, `UPDATE nodes SET status_detail=? WHERE team=? AND run_id=? AND node_id=?`, detail, t.team, ref.RunID, ref.NodeID); err != nil {
			return err
		}
		return nil
	})
}
