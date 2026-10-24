package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

const restoreStateKey = "restore.state"

// ErrRestorePending holds schedule execution until operator acknowledgement.
var ErrRestorePending = errors.New("store: restored schedules are held; run restore acknowledge")

// RestoreState survives restart while a restored deployment is being verified.
type RestoreState struct {
	Epoch          string `json:"epoch"`
	Pending        bool   `json:"pending"`
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
			if err := tx.operator.cancelRestoredSchedulesTx(ctx, tx); err != nil {
				return err
			}
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

func (o *Operator) cancelRestoredSchedulesTx(ctx context.Context, tx *storeTx) error {
	rows, err := tx.QueryContext(ctx, `SELECT team,id FROM runs WHERE trigger_source=? AND finished_at IS NULL ORDER BY team,id`, runspec.OriginSchedule)
	if err != nil {
		return err
	}
	var runs []struct {
		team Team
		id   runspec.RunID
	}
	for rows.Next() {
		var run struct {
			team Team
			id   runspec.RunID
		}
		if err := rows.Scan(&run.team, &run.id); err != nil {
			return errors.Join(err, rows.Close())
		}
		runs = append(runs, run)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	previous := tx.cancelingTeam
	defer func() { tx.cancelingTeam = previous }()
	for _, run := range runs {
		tx.cancelingTeam = run.team
		if _, err := transitionRunTx(ctx, tx, run.team, runstate.RunTransition{RunID: run.id, To: runstate.RunCancelled, Actor: runstate.Actor{Kind: runstate.ActorOperator}, Cause: runstate.RunCancel, Reason: "cancelled by restore; rerun if needed"}); err != nil {
			return err
		}
	}
	return nil
}

// RestoreStatus reports whether restored schedules are held.
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

// AcknowledgeRestore discards schedule catch-up through database time and lifts the hold.
// API/local schedules need fresh consent because rotation invalidates their recorded authority.
// Repeating acknowledgement leaves the first boundary unchanged.
func (o *Operator) AcknowledgeRestore(ctx context.Context) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		state, err := restoreStateTx(ctx, tx)
		if err != nil || !state.Pending {
			return err
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
