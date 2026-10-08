package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// The run sweeps act for every team, so a stale run of a team other than
// the default is failed in its own team rather than stopping the pass.
func TestRunSweepsFinishAStaleRunInItsOwnTeam(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sweep func(*store.Store, context.Context) error
	}{
		{"stale running", func(st *store.Store, ctx context.Context) error {
			_, err := store.Maintenance.ReapStaleRunningRuns(st, ctx, time.Minute, "stale")
			return err
		}},
		{"orphaned local", func(st *store.Store, ctx context.Context) error {
			_, err := store.Maintenance.ReconcileOrphanedLocalRuns(st, ctx, time.Minute)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := storetest.New(t).Open(t)
			acme := tenantFor(t, st, "acme")
			seedTenantRun(t, acme, "run-a", "deploy")
			old := time.Now().Add(-time.Hour).UnixNano()
			if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st,
				`UPDATE runs SET started_at = ?, last_heartbeat_at = ? WHERE id = ?`), old, old, "run-a"); err != nil {
				t.Fatal(err)
			}
			if err := tc.sweep(st, ctx); err != nil {
				t.Fatalf("sweep: %v", err)
			}
			run, err := acme.GetRun(ctx, "run-a")
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != "failed" {
				t.Fatalf("stale acme run status = %q, want failed", run.Status)
			}
		})
	}
}
