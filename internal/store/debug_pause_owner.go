package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// OwnerDebugPause persists a selected owner boundary and reports whether it has been released.
func (t *Tenant) OwnerDebugPause(ctx context.Context, ref runspec.NodeRef, generation int64, actor runstate.Actor, reason string) (released bool, err error) {
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		if err := checkOwnerTx(ctx, tx, t.team, actor, string(ref.RunID)); err != nil {
			return err
		}
		run, err := readRunTx(ctx, tx, t.team, string(ref.RunID))
		if err != nil {
			return err
		}
		row, err := readNodeTx(ctx, tx, t.team, ref)
		if err != nil {
			return err
		}
		if int64(row.Generation) != generation || run.Spec == nil {
			return holderRefusal()
		}
		options := run.Spec.Resolved.Settings.Options
		pause := options.Pause.Value
		selected := reason == PauseReasonBefore && slices.Contains(pause.Before.Value, string(ref.NodeID)) || reason == PauseReasonAfter && slices.Contains(pause.After.Value, string(ref.NodeID)) || reason == PauseReasonOnFailure && pause.OnFailure.Value && row.Outcome == runstate.NodeFailed
		if !selected {
			return ErrInvalidInput
		}
		now := tx.now(ctx)
		var expires int64
		var resolved sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT expires_at,released_at FROM debug_pauses WHERE team=? AND run_id=? AND node_id=? AND reason=?`, t.team, ref.RunID, ref.NodeID, reason).Scan(&expires, &resolved)
		if errors.Is(err, sql.ErrNoRows) {
			if run.CancelRequestedAt != nil || run.FinishedAt != nil {
				released = true
				return nil
			}
			timeout := options.PauseTimeout.Value
			if timeout <= 0 {
				timeout = limits.DebugPause
			}
			expires = now.Add(timeout).UnixNano()
			if _, err = tx.execContext(ctx, `INSERT INTO debug_pauses(team,run_id,node_id,reason,paused_at,expires_at) VALUES (?,?,?,?,?,?)`, t.team, ref.RunID, ref.NodeID, reason, now.UnixNano(), expires); err != nil {
				return err
			}
			if _, err = tx.execContext(ctx, `UPDATE nodes SET status_detail='paused' WHERE team=? AND run_id=? AND node_id=?`, t.team, ref.RunID, ref.NodeID); err != nil {
				return err
			}
			_, err = appendEventTx(ctx, tx, t.team, string(ref.RunID), string(ref.NodeID), runstate.EventDebugPaused, map[string]any{"reason": reason, "expires_at": now.Add(timeout)}, now, true)
			return err
		}
		if err != nil {
			return err
		}
		if resolved.Valid {
			released = true
			return nil
		}
		kind := ""
		if run.CancelRequestedAt != nil || run.FinishedAt != nil {
			kind = "cancelled"
		} else if expires <= now.UnixNano() {
			kind = "timeout"
		}
		if kind == "" {
			return nil
		}
		if _, err = tx.execContext(ctx, `UPDATE debug_pauses SET released_at=?,release_kind=? WHERE team=? AND run_id=? AND node_id=? AND reason=? AND released_at IS NULL`, now.UnixNano(), kind, t.team, ref.RunID, ref.NodeID, reason); err != nil {
			return err
		}
		if _, err = tx.execContext(ctx, `UPDATE nodes SET status_detail='' WHERE team=? AND run_id=? AND node_id=? AND status_detail='paused'`, t.team, ref.RunID, ref.NodeID); err != nil {
			return err
		}
		_, err = appendEventTx(ctx, tx, t.team, string(ref.RunID), string(ref.NodeID), runstate.EventDebugResumed, map[string]string{"release_kind": kind}, now, true)
		released = err == nil
		return err
	})
	return released, err
}
