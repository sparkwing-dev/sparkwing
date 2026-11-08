package store

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestReaperCancelsChildrenTheirParentNoLongerAwaits(t *testing.T) {
	reaper := runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController}}
	for _, parent := range []string{"live", "manual_retry", "run_cancelled", "run_finished", "job_finished"} {
		t.Run(parent, func(t *testing.T) {
			tenant, request := childIntakeFixture(t)
			ctx := t.Context()
			child, err := tenant.AdmitRun(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			switch parent {
			case "manual_retry":
				// safety: a retry keeps its source's parent lineage after that parent is gone.
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET retry_of='earlier-child' WHERE id=?`, child.RunID)
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET status='failed',finished_at=1,cancel_requested_at=1 WHERE id='run'`)
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
			if parent == "live" || parent == "manual_retry" {
				if row.CancelRequestedAt != nil {
					t.Fatalf("reaper cancelled a %s child: %q", parent, row.CancelReason)
				}
				return
			}
			if row.CancelRequestedAt == nil {
				t.Fatalf("child %s outlived its %s parent", child.RunID, parent)
			}
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

func TestUnawaitedChildSweepReadsOnlyLiveChildren(t *testing.T) {
	tenant, _ := childIntakeFixture(t)
	if tenant.s.dialect != DialectSQLite {
		t.Skip("plan text is SQLite's")
	}
	rows, err := tenant.s.query(t.Context(), "EXPLAIN QUERY PLAN "+tenant.s.unawaitedChildrenQuery(), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// perf: the sweep must start from idx_runs_live_children, not a scan of run history.
	if len(plan) == 0 || plan[0] != "SCAN c USING INDEX idx_runs_live_children" {
		t.Fatalf("sweep plan does not start from the live child index:\n%s", strings.Join(plan, "\n"))
	}
}

func TestDetachedChildrenSurviveFinishedOrCancelledParents(t *testing.T) {
	for _, parent := range []string{"run_finished", "job_finished", "run_cancelled"} {
		t.Run(parent, func(t *testing.T) {
			tenant, request := childIntakeFixture(t)
			request.Request.Lineage.Detached = true
			child, err := tenant.AdmitRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch parent {
			case "run_finished":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET status='success',finished_at=1 WHERE id='run'`)
			case "job_finished":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='done',outcome='success',finished_at=1,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE run_id='run' AND node_id='job'`)
			case "run_cancelled":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET status='cancelled',finished_at=1,cancel_requested_at=1 WHERE id='run'`)
			}
			for range 2 {
				if _, err := tenant.s.AsOperator().ReapUnits(t.Context(), 1); err != nil {
					t.Fatal(err)
				}
			}
			row, err := tenant.ReadRun(t.Context(), child.RunID)
			if err != nil || row.CancelRequestedAt != nil || !row.Lineage.Detached {
				t.Fatalf("detached child after %s: state %s, cancel %v, detached %t, error %v", parent, row.State, row.CancelRequestedAt, row.Lineage.Detached, err)
			}
		})
	}
}
