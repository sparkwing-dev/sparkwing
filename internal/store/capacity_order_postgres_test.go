package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestPostgresControllerAdmissionDefersContendedGroupWithoutWaitingOnRunLock(t *testing.T) {
	for _, held := range []string{"group", "foreign_run", "foreign_node"} {
		t.Run(held, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, true, storetest.NewPostgres(t), func(plan *nodeproto.PlanDoc) {
				scope, capacity, cost, policy := "global", 1, 1, "queue"
				plan.Concurrency = []nodeproto.ConcurrencySpec{{Group: "plan", Scope: &scope, Capacity: &capacity, Cost: &cost, OnLimit: &policy}}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			lease, err := f.s.AsOperator().AcquireSingletonLease(ctx, "controller.owner", "controller", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.DB().ExecContext(ctx, `UPDATE runs SET dispatch='controller' WHERE team=$1 AND id=$2`, f.tenant.Team(), f.ref.RunID); err != nil {
				t.Fatal(err)
			}
			competitor := ""
			if held != "group" {
				competitor, err = f.capacityCompetitor(ctx)
				if err != nil {
					t.Fatal(err)
				}
				node := ""
				if held == "foreign_node" {
					node = "job"
				}
				result, err := f.tenant.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{Key: "plan:global:plan", HolderID: "competitor", RunID: competitor, NodeID: node, Capacity: 1, Cost: 1, Lease: time.Minute, Policy: store.OnLimitQueue})
				if err != nil || result.Kind != store.AcquireGranted {
					t.Fatalf("competitor: %+v, %v", result, err)
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
			case "foreign_node":
				_, err = blocked.ExecContext(ctx, `SELECT node_id FROM nodes WHERE team=$1 AND run_id=$2 AND node_id='job' FOR UPDATE`, f.tenant.Team(), competitor)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.AsOperator().ReconcileControllerRuns(ctx, lease, "", 10); err != nil {
				t.Fatalf("controller waited for an admission transaction while holding its run: %v", err)
			}
			var count int
			if err := f.s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM concurrency_holders WHERE team=$1 AND run_id=$2`, f.tenant.Team(), f.ref.RunID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("contended admission mutated reservations: count=%d, %v", count, err)
			}
		})
	}
}
