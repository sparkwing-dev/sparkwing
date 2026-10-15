package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ResumeAuthorizedDebugPause closes open pauses without changing job lifecycle or execution fences.
func (t *Tenant) ResumeAuthorizedDebugPause(ctx context.Context, authorized capability.Authorized) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		target := authorized.Target
		if target.Team != string(t.team) || target.RunID == "" || target.NodeID == "" {
			return bindingRefusal()
		}
		principal, err := tx.ResolveAuthority(ctx, authorized.Principal.Authority)
		if err != nil {
			return err
		}
		route, found := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/resume")
		if !found || len(route.Access) != 1 || route.Access[0].Liveness != capability.LiveCredential {
			return authorityError(apierr.PolicyMissing, "resume policy is unavailable")
		}
		current, err := capability.Authorize(ctx, tx, route, capability.Authenticated{Principal: principal, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, target)
		if err != nil {
			return err
		}
		if err := lockStateRunTx(ctx, tx, t.team, target.RunID); err != nil {
			return err
		}
		row, err := readNodeTx(ctx, tx, t.team, NodeRef{RunID: target.RunID, NodeID: target.NodeID})
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE debug_pauses SET released_at=?,released_by=?,release_kind=? WHERE team=? AND run_id=? AND node_id=? AND released_at IS NULL`, tx.now().UnixNano(), current.Principal.Capability.Subject.ID, PauseReleaseManual, t.team, target.RunID, target.NodeID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		if row.StatusDetail == "paused" {
			if _, err := tx.ExecContext(ctx, `UPDATE nodes SET status_detail='' WHERE team=? AND run_id=? AND node_id=? AND status_detail='paused'`, t.team, target.RunID, target.NodeID); err != nil {
				return err
			}
		}
		_, err = appendEventTx(ctx, tx, t.team, target.RunID, target.NodeID, "debug.resumed", map[string]string{"released_by": current.Principal.Capability.Subject.ID}, tx.now())
		return err
	})
}
