package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestReaperCancelsChildrenTheirParentNoLongerAwaits(t *testing.T) {
	reaper := runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController}}
	for _, parent := range []string{"live", "run_cancelled", "run_finished", "job_finished"} {
		t.Run(parent, func(t *testing.T) {
			tenant, request := childIntakeFixture(t)
			ctx := t.Context()
			child, err := tenant.AdmitRun(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			switch parent {
			case "run_cancelled":
				if _, err := tenant.TransitionRun(ctx, runstate.RunTransition{RunID: "run", To: runstate.RunCancelled, Cause: runstate.RunCancel, Actor: reaper, Reason: "operator"}); err != nil {
					t.Fatal(err)
				}
			case "run_finished":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET status='failed',finished_at=1 WHERE id='run'`)
			case "job_finished":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='done',outcome='failed',finished_at=1,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE run_id='run' AND node_id='job'`)
			}
			if _, err := tenant.s.AsOperator().ReapUnits(ctx, 100); err != nil {
				t.Fatal(err)
			}
			row, err := tenant.ReadRun(ctx, child.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if parent == "live" {
				if row.CancelRequestedAt != nil {
					t.Fatalf("reaper cancelled a child its live parent awaits: %q", row.CancelReason)
				}
				return
			}
			if row.CancelRequestedAt == nil {
				t.Fatalf("child %s outlived its %s parent", child.RunID, parent)
			}
			// The next sweep finishes the unstarted child through its owner unit.
			if _, err := tenant.s.AsOperator().ReapUnits(ctx, 100); err != nil {
				t.Fatal(err)
			}
			if row, err = tenant.ReadRun(ctx, child.RunID); err != nil || row.State != runstate.RunCancelled {
				t.Fatalf("child state %q: %v", row.State, err)
			}
			if _, err := tenant.s.AsOperator().ReapUnits(ctx, 100); err != nil {
				t.Fatalf("sweep after the child ended: %v", err)
			}
		})
	}
}
