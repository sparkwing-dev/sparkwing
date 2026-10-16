package store_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestStoreRecoveryLocalOrphans(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprintf("started=%t", started), func(t *testing.T) {
			ctx := t.Context()
			f := &storeRecoveryFixture{target: storetest.NewSQLite(t)}
			var err error
			f.s, err = f.target.TryOpen()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := f.s.Close(); err != nil {
					t.Error(err)
				}
			})
			f.tenant, f.plan, err = storePlanSealRequestWith(ctx, f.s, func(input *store.IntakeRequest, doc *nodeproto.PlanDoc) {
				input.Request.Origin = runspec.OriginLocal
				doc.Jobs = []nodeproto.JobSpec{}
			})
			if err != nil {
				t.Fatal(err)
			}
			f.owner, f.ref = f.plan.Actor, store.NodeRef{RunID: f.plan.RunID}
			if _, err := f.tenant.AcceptPlan(ctx, f.plan); err != nil {
				t.Fatal(err)
			}
			if _, err := f.tenant.TransitionRun(ctx, store.RunTransition{RunID: f.ref.RunID, Actor: f.owner, To: store.RunRunning, Cause: store.RunStart}); err != nil {
				t.Fatal(err)
			}
			before, err := f.tenant.ReadRun(ctx, f.ref.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.RenewUnit(ctx, f.owner.CapabilityPrefix, started); err != nil {
				t.Fatal(err)
			}
			f.lease, err = f.s.AsOperator().AcquireSingletonLease(ctx, "controller.reaper", "recovery", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			now, err := f.s.TestOnlyDatabaseTime(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.DB().ExecContext(ctx, `UPDATE triggers SET lease_expires_at=? WHERE team=? AND id=?`, now.UnixNano(), f.tenant.Team(), f.ref.RunID); err != nil {
				t.Fatal(err)
			}
			for pass := range 2 {
				units, err := f.s.ReapUnits(ctx, f.lease, 10)
				if err != nil {
					t.Fatal(err)
				}
				if pass == 1 {
					if len(units) != 0 {
						t.Fatalf("repeated orphan pass: %+v", units)
					}
					continue
				}
				if len(units) != 1 || units[0].RunID != f.ref.RunID || units[0].Kind != "trigger" || units[0].Generation != f.owner.Owner.Generation || units[0].Failed != started || units[0].Requeued == started || units[0].Cancelled {
					t.Fatalf("orphan recovery: %+v", units)
				}
			}
			if err := f.reopen(ctx); err != nil {
				t.Fatal(err)
			}
			run, err := f.tenant.ReadRun(ctx, f.ref.RunID)
			if err != nil {
				t.Fatal(err)
			}
			wantState, wantOwner, wantEvent, disruptions := store.RunRunning, "pending", "run_requeued", 0
			if started {
				wantState, wantOwner, wantEvent, disruptions = store.RunFailed, "failed", "run_failed", 1
			}
			if run.State != wantState || (run.FinishedAt != nil) != started || run.Number != before.Number {
				t.Fatalf("reopened run: %+v", run)
			}
			var status string
			var lost int
			var generation int64
			var holder sql.NullString
			var lease, execution sql.NullInt64
			if err := f.s.DB().QueryRowContext(ctx, `SELECT status,generation,disruptions,claimed_by,lease_expires_at,started_at FROM triggers WHERE team=? AND id=?`, f.tenant.Team(), f.ref.RunID).Scan(&status, &generation, &lost, &holder, &lease, &execution); err != nil {
				t.Fatal(err)
			}
			if status != wantOwner || generation != f.owner.Owner.Generation || lost != disruptions || holder.Valid || lease.Valid || execution.Valid != started {
				t.Fatalf("owner: status=%s generation=%d disruptions=%d holder=%v lease=%v started=%v", status, generation, lost, holder, lease, execution)
			}
			events, err := f.tenant.ReadStateEvents(ctx, f.ref.RunID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			var changes []store.StateEvent
			for _, event := range events {
				if event.NodeID == "" && event.StateSeq > before.StateSeq {
					changes = append(changes, event)
				}
			}
			if len(changes) != 1 || changes[0].Kind != wantEvent || changes[0].StateSeq != run.StateSeq {
				t.Fatalf("recovery events: %+v", changes)
			}
			for _, table := range []string{"runs", "triggers"} {
				var count int
				if err := f.s.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE team=?`, f.tenant.Team()).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatalf("%s count=%d", table, count)
				}
			}
		})
	}
}
