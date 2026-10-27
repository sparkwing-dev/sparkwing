package store

import (
	"context"

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

// LogSlot is what a signed log upload may store: its byte size, fixed when first signed, and whether its object is
// already committed, in which case the uploader has nothing left to send.
type LogSlot struct {
	RetentionClass string
	Size           int64
	Committed      bool
}

// OpenLogSlot checks the writer as PrepareLogWrite does and, before the caller signs an upload URL, admits a new
// slot's bytes through the storage rule. They count as retained from then until Sparkwing deletes the object,
// whether or not it is ever committed, so a refused team gets no URL and an abandoned upload still counts. Signing the slot
// again returns its first size, never another.
func (t *Tenant) OpenLogSlot(ctx context.Context, subject blob.LogSubject, actor runstate.Actor, index, size int64) (LogSlot, error) {
	if subject.Team != string(t.team) {
		return LogSlot{}, authorityError(apierr.Forbidden, "log belongs to another team")
	}
	if err := subject.Validate(); err != nil {
		return LogSlot{}, authorityError(apierr.BadRequest, "invalid log subject")
	}
	identity, err := logIdentity(subject)
	if err != nil {
		return LogSlot{}, err
	}
	if index < 0 || size < 1 {
		return LogSlot{}, authorityError(apierr.BadRequest, "invalid log upload slot")
	}
	var out LogSlot
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		if out.RetentionClass, err = t.prepareLogWriteTx(ctx, tx, subject, actor); err != nil {
			return err
		}
		slot, err := logSlotTx(ctx, tx, t.team, subject.RunID, identity.id, index)
		if err != nil {
			return err
		}
		if slot.found {
			out.Size, out.Committed = slot.size, slot.committed
			return nil
		}
		key, err := blob.LogObjectKey(subject, out.RetentionClass, index)
		if err != nil {
			return err
		}
		out.Size = size
		return openLogSlotTx(ctx, tx, t.team, subject.RunID, identity, index, size, key, false)
	})
	return out, err
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
