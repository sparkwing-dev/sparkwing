package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestLauncherSyncDiagnosticClearsWhenUnstartedPlanRequeues(t *testing.T) {
	for _, failure := range []string{launcherSyncFailure, "pipeline failure"} {
		t.Run(failure, func(t *testing.T) {
			s, demand, now := meteredUnitFixture(t, true)
			demand.Target = &UnitRef{Kind: capability.UnitPlan, RunID: "run"}
			rewriteSchemaExec(t, s.db, `DELETE FROM nodes`)
			rewriteSchemaExec(t, s.db, `UPDATE runs SET status='pending',dispatch='controller',error=?`, failure)
			rewriteSchemaExec(t, s.db, `UPDATE triggers SET unit_kind='plan'`)
			claim, _ := meteredUnitClaim(t, s, demand)
			*now = claim.Award.LeaseExpiresAt
			reaped, err := s.AsOperator().ReapUnits(t.Context(), 10)
			if err != nil || len(reaped) != 1 || !reaped[0].Requeued {
				t.Fatalf("plan did not requeue: %+v, %v", reaped, err)
			}
			view, err := (&Tenant{s: s, team: "a"}).ReadRunView(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			if failure == launcherSyncFailure {
				if len(view.Failures) != 0 {
					t.Fatalf("requeued plan retained launcher diagnostic: %+v", view.Failures)
				}
			} else if len(view.Failures) != 1 || view.Failures[0].Error != failure {
				t.Fatalf("requeue erased an execution failure: %+v", view.Failures)
			}
		})
	}
}
