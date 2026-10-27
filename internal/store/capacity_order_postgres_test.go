package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestPostgresControllerAdmissionDefersContendedGroupWithoutWaitingOnRunLock(t *testing.T) {
	for _, held := range []string{"group", "foreign_run"} {
		t.Run(held, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, true, storetest.NewPostgres(t), func(plan *nodeproto.PlanDoc) {
				scope, capacity, cost, policy := "global", 1, 1, "queue"
				plan.Concurrency = []nodeproto.ConcurrencySpec{{Group: "plan", Scope: &scope, Capacity: &capacity, Cost: &cost, OnLimit: &policy}}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			if _, err := f.s.DB().ExecContext(ctx, `UPDATE runs SET dispatch='controller' WHERE team=$1 AND id=$2`, f.tenant.Team(), f.ref.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.DB().ExecContext(ctx, `DELETE FROM concurrency_holders WHERE team=$1 AND run_id=$2 AND node_id=''`, f.tenant.Team(), f.ref.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.DB().ExecContext(ctx, `UPDATE admission_work SET granted=0,lease_expires_at=1 WHERE team=$1 AND run_id=$2 AND node_id=''`, f.tenant.Team(), f.ref.RunID); err != nil {
				t.Fatal(err)
			}
			competitor := ""
			if held != "group" {
				admitted, err := f.tenant.AdmitRun(ctx, store.IntakeRequest{Authority: f.plan.Seal.Request.AuthorityRef, Request: f.plan.Seal.Request.Request})
				if err != nil {
					t.Fatal(err)
				}
				competitor = admitted.RunID
				owner, err := f.claimOwner(ctx, competitor, "competitor")
				if err != nil {
					t.Fatal(err)
				}
				plan := f.plan
				plan.RunID, plan.Actor, plan.Seal.Request = competitor, owner, admitted.Request
				if _, err := f.tenant.AcceptPlan(ctx, plan); err != nil {
					t.Fatal(err)
				}
				if granted, err := f.tenant.MayStart(ctx, runstate.CapacityBinding{RunID: competitor, Generation: int64(owner.Owner.Generation)}, owner); err != nil || granted.Kind != runstate.AcquireGranted {
					t.Fatalf("competitor plan admission: %+v, %v", granted, err)
				}

			}
			blocked, err := f.s.DB().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocked.Rollback()
			switch held {
			case "group":
				name := fmt.Sprintf("capacity-admission:%d:%s:%s", len(f.tenant.Team()), f.tenant.Team(), "plan:global:plan")
				_, err = blocked.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, name)
			case "foreign_run":
				_, err = blocked.ExecContext(ctx, `SELECT id FROM runs WHERE team=$1 AND id=$2 FOR NO KEY UPDATE`, f.tenant.Team(), competitor)

			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.AsOperator().ReconcileControllerRuns(ctx, "", 10); err != nil {
				t.Fatalf("controller waited for an admission transaction while holding its run: %v", err)
			}
			var count int
			if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_holders WHERE team=$1 AND run_id=$2`, f.tenant.Team(), f.ref.RunID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("contended admission mutated reservations: count=%d, %v", count, err)
			}
		})
	}
}

func TestPostgresConcurrencyMaintenanceDefersBusyAdmission(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, false, storetest.NewPostgres(t), func(plan *nodeproto.PlanDoc) {
		plan.Concurrency = []nodeproto.ConcurrencySpec{{Group: "first"}, {Group: "second"}}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE concurrency_holders SET lease_expires_at=1 WHERE team=$1 AND run_id=$2`, f.tenant.Team(), f.ref.RunID); err != nil {
		t.Fatal(err)
	}
	admission, err := f.s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Rollback()
	if _, err := admission.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "admission:"+string(f.tenant.Team())); err != nil {
		t.Fatal(err)
	}
	result, err := f.s.AsOperator().MaintainConcurrency(ctx)
	if err != nil || len(result.StaleHolders) != 0 || len(result.StaleWaiters) != 0 {
		t.Fatalf("maintenance competed with admission: %+v %v", result, err)
	}
	var count int
	if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_holders WHERE team=$1 AND run_id=$2`, f.tenant.Team(), f.ref.RunID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("busy admission lost reservations: count=%d %v", count, err)
	}
	if err := admission.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err = f.s.AsOperator().MaintainConcurrency(ctx)
	if err != nil || len(result.StaleHolders) == 0 {
		t.Fatalf("maintenance did not resume: %+v %v", result, err)
	}
	if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_holders WHERE team=$1 AND run_id=$2`, f.tenant.Team(), f.ref.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial cleanup: count=%d %v", count, err)
	}
}
