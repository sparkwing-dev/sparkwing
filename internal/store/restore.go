package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

const restoreStateKey = "restore.state"

// ErrRestorePending holds schedule execution until reconciliation and operator acknowledgement.
var ErrRestorePending = errors.New("store: restored schedules are held; run restore reconcile, then restore acknowledge")

// RestoreState survives restart while a restored deployment is being verified.
type RestoreState struct {
	Epoch          string `json:"epoch"`
	Pending        bool   `json:"pending"`
	Reconciled     bool   `json:"reconciled"`
	AcknowledgedAt int64  `json:"acknowledged_at,omitempty"`
}

// EnsureCredentialEpoch detects an external fence changed across a database restore.
// Keeping the last observed epoch in the database closes the crash window between rotation and preparation.
func (s *Store) EnsureCredentialEpoch(ctx context.Context) error {
	return s.ensureCredentialEpoch(ctx, s.credentialEpoch, false)
}

// PrepareRestore records an epoch rotated while the controller is stopped.
func (o *Operator) PrepareRestore(ctx context.Context, epoch string) error {
	if epoch == "" || epoch == o.s.credentialEpoch {
		return errors.New("restore preparation requires a newly rotated credential epoch")
	}
	return o.s.ensureCredentialEpoch(ctx, epoch, true)
}

func (s *Store) ensureCredentialEpoch(ctx context.Context, epoch string, prepared bool) error {
	if epoch == "" {
		return nil
	}
	return s.inTx(ctx, func(tx *storeTx) error {
		var previous []byte
		err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='credential_epoch'`+tx.forUpdate()).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if prepared || err == nil && string(previous) != epoch {
			if err := writeRestoreStateTx(ctx, tx, RestoreState{Epoch: epoch, Pending: true}); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key=?`, []byte(""), bootstrapUsedKey); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES ('credential_epoch',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, []byte(epoch))
		return err
	})
}

// RestoreStatus reports whether schedules are held and the provider sweep completed.
func (o *Operator) RestoreStatus(ctx context.Context) (RestoreState, error) {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return RestoreState{}, err
	}
	defer rollbackOrLog(tx)
	return restoreStateTx(ctx, tx)
}

func restoreStateTx(ctx context.Context, tx *storeTx) (RestoreState, error) {
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, restoreStateKey).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return RestoreState{}, nil
	}
	if err != nil {
		return RestoreState{}, err
	}
	var state RestoreState
	if err := json.Unmarshal(body, &state); err != nil {
		return state, fmt.Errorf("restore state: %w", err)
	}
	return state, nil
}

func writeRestoreStateTx(ctx context.Context, tx *storeTx, state RestoreState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, restoreStateKey, body)
	return err
}

// ReconcileRestore records a successful provider pull without holding a transaction across network calls.
func (o *Operator) ReconcileRestore(ctx context.Context, pull func(context.Context) error) error {
	state, err := inTxResult(ctx, o.s, func(tx *storeTx) (RestoreState, error) {
		state, err := restoreStateTx(ctx, tx)
		if err != nil {
			return state, err
		}
		if !state.Pending || pull == nil {
			return state, errors.New("restore reconciliation requires a prepared restore and a provider sweep")
		}
		// A failed or interrupted resweep must invalidate an earlier success.
		state.Reconciled = false
		return state, writeRestoreStateTx(ctx, tx, state)
	})
	if err != nil {
		return err
	}
	if err := pull(ctx); err != nil {
		return err
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		current, err := restoreStateTx(ctx, tx)
		if err != nil {
			return err
		}
		if !current.Pending || current.Epoch != state.Epoch {
			return errors.New("restore changed during provider reconciliation")
		}
		current.Reconciled = true
		return writeRestoreStateTx(ctx, tx, current)
	})
}

// AcknowledgeRestore discards schedule catch-up through database time and resumes future instants.
// Repeating acknowledgement leaves the first boundary unchanged.
func (o *Operator) AcknowledgeRestore(ctx context.Context) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		state, err := restoreStateTx(ctx, tx)
		if err != nil || !state.Pending {
			return err
		}
		if !state.Reconciled {
			return errors.New("restore requires successful provider reconciliation before schedules can resume")
		}
		now := tx.now(ctx).UnixNano()
		if _, err := tx.ExecContext(ctx, `UPDATE cron_schedules SET cursor_at=`+o.s.greatest()+`(cursor_at,?),next_due_at=NULL,updated_at=?`, now, now); err != nil {
			return err
		}
		state.Pending, state.AcknowledgedAt = false, now
		return writeRestoreStateTx(ctx, tx, state)
	})
}

func checkRestoreScheduleTx(ctx context.Context, tx *storeTx, due time.Time) error {
	state, err := restoreStateTx(ctx, tx)
	if err != nil {
		return err
	}
	if state.Pending || !due.IsZero() && state.AcknowledgedAt > 0 && due.UnixNano() <= state.AcknowledgedAt {
		return ErrRestorePending
	}
	return nil
}

// RestorePaymentMode uses the restored database's mode rather than accepting a replacement from an operator flag.
func (o *Operator) RestorePaymentMode(ctx context.Context) (capability.PaymentMode, error) {
	var body []byte
	err := o.s.queryRow(ctx, `SELECT value FROM settings WHERE key='payment_mode'`).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var mode capability.PaymentMode
	if json.Unmarshal(body, &mode) != nil || mode != capability.PaymentLive && mode != capability.PaymentTest {
		return "", errors.New("restore payment mode is invalid")
	}
	return mode, nil
}
