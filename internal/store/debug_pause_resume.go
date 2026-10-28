package store

import (
	"context"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ResumeAuthorizedDebugPause closes open pauses without changing job lifecycle or execution fences.
func (t *Tenant) ResumeAuthorizedDebugPause(ctx context.Context, authorized capability.Authorized) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		target := authorized.Target
		if target.Team != string(t.team) || target.RunID == "" || target.NodeID == "" {
			return bindingRefusal()
		}
		current, err := t.runControlAuthorityTx(ctx, tx, authorized, "/api/v1/runs/{run}/jobs/{job}/resume")
		if err != nil {
			return err
		}

		row, err := readNodeTx(ctx, tx, t.team, runspec.NodeRef{RunID: runspec.RunID(target.RunID), NodeID: runspec.NodeID(target.NodeID)})
		if err != nil {
			return err
		}
		result, err := tx.execContext(ctx, `UPDATE debug_pauses SET released_at=?,released_by=?,release_kind=? WHERE team=? AND run_id=? AND node_id=? AND released_at IS NULL`, tx.now(ctx).UnixNano(), current.Capability.Subject.ID, PauseReleaseManual, t.team, target.RunID, target.NodeID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		if row.StatusDetail == "paused" {
			if _, err := tx.execContext(ctx, `UPDATE nodes SET status_detail='' WHERE team=? AND run_id=? AND node_id=? AND status_detail='paused'`, t.team, target.RunID, target.NodeID); err != nil {
				return err
			}
		}
		_, err = appendEventTx(ctx, tx, t.team, target.RunID, target.NodeID, runstate.EventDebugResumed, map[string]string{"released_by": current.Capability.Subject.ID}, tx.now(ctx), true)
		return err
	})
}
