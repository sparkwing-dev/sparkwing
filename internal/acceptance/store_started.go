package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkRecoveryStartedCap(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "started.db")
	f, err := newStoreRecoveryFixture(ctx, path, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	ref := store.NodeRef{RunID: "run", NodeID: "job"}
	var kinds []string
	for ordinal := 1; ordinal <= 3; ordinal++ {
		started, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: ref, Actor: f.holder, To: store.NodeRunning, Cause: store.CauseStart})
		if err != nil {
			return err
		}
		if started.Attempt != int64(ordinal) || started.Generation != f.holder.Generation {
			return fmt.Errorf("execution ordinal or generation reset: %+v", started)
		}
		kinds = append(kinds, "job_started")
		if err := f.expireHolder(ctx); err != nil {
			return err
		}
		units, err := f.s.ReapUnits(ctx, f.lease, 10)
		if err != nil {
			return err
		}
		if len(units) != 1 || units[0].Disruptions != ordinal || units[0].Requeued != (ordinal < 3) || units[0].Failed != (ordinal == 3) {
			return fmt.Errorf("started lapse policy: %+v", units)
		}
		if err := f.reopen(ctx, path); err != nil {
			return err
		}
		if err := f.refuseLateReports(ctx); err != nil {
			return err
		}
		row, err := f.tenant.ReadNode(ctx, ref)
		if err != nil {
			return err
		}
		if row.Attempt != int64(ordinal) || row.ApplicationRetries != 0 || row.Disruptions != ordinal || row.ClaimedBy != "" || row.CapabilityPrefix != "" || row.LeaseExpiresAt != nil || row.Generation != started.Generation {
			return fmt.Errorf("lapse changed execution history or kept a claim: %+v", row)
		}
		var outcome, reason string
		var finished int64
		if err := f.s.DB().QueryRowContext(ctx, `SELECT outcome,failure_reason,finished_at FROM node_attempts WHERE team=? AND run_id='run' AND node_id='job' AND attempt=? AND generation=?`, store.DefaultTeam, ordinal, started.Generation).Scan(&outcome, &reason, &finished); err != nil {
			return err
		}
		if outcome != "failed" || reason != "agent_lost" || finished <= 0 {
			return fmt.Errorf("lost attempt %d: %s/%s at %d", ordinal, outcome, reason, finished)
		}
		next, err := f.tenant.ReconcileNode(ctx, store.ReconcileRequest{Ref: ref, Actor: f.owner, Policy: store.SourceResume})
		if err != nil {
			return err
		}
		if ordinal == 3 {
			if next.Kind != store.KeepTerminal || next.Claim != nil || len(next.Bearer) != 0 || row.Outcome != store.NodeFailed || row.FinishedAt == nil {
				return fmt.Errorf("disruption cap allowed another execution: %+v", next)
			}
			kinds = append(kinds, "job_failed")
			continue
		}
		if next.Kind != store.Execute || next.Claim == nil || len(next.Bearer) == 0 || next.Claim.Generation != started.Generation+1 || next.Node.Attempt != int64(ordinal) {
			return fmt.Errorf("started lapse did not reclaim its next generation: %+v", next)
		}
		kinds = append(kinds, "job_requeued", "job_ready", "job_claimed")
		f.holder = store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: next.Claim.CapabilityPrefix}, CapabilityPrefix: next.Claim.CapabilityPrefix, Holder: next.Claim.Holder, Generation: next.Claim.Generation}
	}
	var attempts, runs int
	if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM node_attempts WHERE team=?`, store.DefaultTeam).Scan(&attempts); err != nil {
		return err
	}
	if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE team=?`, store.DefaultTeam).Scan(&runs); err != nil {
		return err
	}
	if attempts != 3 || runs != 1 {
		return fmt.Errorf("started lapses created attempts=%d runs=%d", attempts, runs)
	}
	return checkRecoveryEvents(ctx, f, kinds)
}
