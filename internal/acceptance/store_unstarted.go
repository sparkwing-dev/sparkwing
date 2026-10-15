package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkRecoveryUnstarted(ctx context.Context, root string) (err error) {
	if err := checkOwnerUnstartedRecovery(ctx, root); err != nil {
		return err
	}
	path := filepath.Join(root, "unstarted.db")
	f, err := newStoreRecoveryFixture(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	ref := store.NodeRef{RunID: "run", NodeID: "job"}
	var kinds []string
	for range 5 {
		before, err := f.tenant.ReadNode(ctx, ref)
		if err != nil {
			return err
		}
		if err := f.expireHolder(ctx); err != nil {
			return err
		}
		units, err := f.s.ReapUnits(ctx, f.lease, 10)
		if err != nil {
			return err
		}
		if len(units) != 1 || !units[0].Requeued || units[0].Disruptions != 0 || units[0].Generation != before.Generation {
			return fmt.Errorf("unstarted lapse consumed recovery budget: %+v", units)
		}
		kinds = append(kinds, "job_requeued")
		if err := f.reopen(ctx, path); err != nil {
			return err
		}
		pending, err := f.tenant.ReadNode(ctx, ref)
		if err != nil {
			return err
		}
		if pending.Status != store.StatusPending || pending.ClaimedBy != "" || pending.CapabilityPrefix != "" || pending.LeaseExpiresAt != nil || pending.StartedAt != nil || pending.Attempt != 0 || pending.Disruptions != 0 || pending.ApplicationRetries != 0 || pending.Generation != before.Generation {
			return fmt.Errorf("unstarted requeue lost its execution history: %+v", pending)
		}
		if err := f.refuseLateReports(ctx); err != nil {
			return err
		}
		next, err := f.tenant.ReconcileNode(ctx, store.ReconcileRequest{Ref: ref, Actor: f.owner, Policy: store.SourceResume})
		if err != nil {
			return err
		}
		if next.Kind != store.Execute || next.Claim == nil || len(next.Bearer) == 0 || next.Node.Attempt != 0 || next.Claim.Generation != before.Generation+1 {
			return fmt.Errorf("unstarted work did not reclaim without an attempt: %+v", next)
		}
		kinds = append(kinds, "job_ready", "job_claimed")
		f.holder = store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: next.Claim.CapabilityPrefix}, CapabilityPrefix: next.Claim.CapabilityPrefix, Holder: next.Claim.Holder, Generation: next.Claim.Generation}
	}
	applied, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: ref, Actor: f.holder, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: recoverySourceAttempt(0)})
	if err != nil {
		return err
	}
	if applied.Attempt != 1 || applied.Generation != f.holder.Generation {
		return fmt.Errorf("first execution after free lapses has ordinal %d generation %d", applied.Attempt, applied.Generation)
	}
	var attempts, runs int
	if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM node_attempts WHERE team=? AND run_id='run' AND node_id='job'`, store.DefaultTeam).Scan(&attempts); err != nil {
		return err
	}
	if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE team=?`, store.DefaultTeam).Scan(&runs); err != nil {
		return err
	}
	if attempts != 1 || runs != 1 {
		return fmt.Errorf("unstarted lapses created attempts=%d runs=%d", attempts, runs)
	}
	return checkRecoveryEvents(ctx, f, append(kinds, "job_started"))
}
