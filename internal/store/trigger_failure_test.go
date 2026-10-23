package store_test

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestFailedRunClosesOwnerAndKeepsVerdict(t *testing.T) {
	st := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies))
	tenant, plan, err := storePlanSealRequest(t.Context(), st)
	if err != nil {
		t.Fatal(err)
	}
	const reason = "pipeline missing is not defined in acme/web@abc; defined: build"
	finish := runstate.FinishRunRequest{Unit: plan.Actor.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(plan.RunID), Actor: plan.Actor, Cause: runstate.RunFinish, MaskedError: reason}}
	for replay := range 2 {
		applied, err := tenant.FinishRunAndUnit(t.Context(), finish)
		if err != nil || applied.Replayed != (replay == 1) {
			t.Fatalf("completion: %+v, %v", applied, err)
		}
	}
	var status, verdict, failure string
	var lease sql.NullInt64
	if err := st.DB().QueryRowContext(t.Context(), `SELECT t.status,r.status,r.error,t.lease_expires_at FROM triggers t JOIN runs r ON r.team=t.team AND r.id=t.id WHERE t.team=? AND t.id=?`, tenant.Team(), finish.Unit.RunID).Scan(&status, &verdict, &failure, &lease); err != nil || status != "done" || verdict != "failed" || failure != reason || lease.Valid {
		t.Fatalf("failed run: owner=%s verdict=%s error=%q lease=%v, %v", status, verdict, failure, lease, err)
	}
	before, err := tenant.ReadSnapshot(t.Context(), string(finish.Unit.RunID))
	if err != nil {
		t.Fatal(err)
	}

	if applied, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil || !applied.Replayed {
		t.Fatalf("failure completion did not replay: %+v %v", applied, err)
	}
	after, err := tenant.ReadSnapshot(t.Context(), string(finish.Unit.RunID))
	before.StoreNow = after.StoreNow
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("conflicting completion changed failed run: %v", err)
	}
}

func TestFinishWithoutAcceptedPlanDerivesFailure(t *testing.T) {
	st := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies))
	tenant, plan, err := storePlanSealRequest(t.Context(), st)
	if err != nil {
		t.Fatal(err)
	}
	// A raw run edge cannot bypass the same verdict owner as atomic finish.
	if _, err := tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(plan.RunID), Actor: plan.Actor, Cause: runstate.RunFinish, To: runstate.RunSuccess}); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadRun(t.Context(), plan.RunID)
	if err != nil || row.State != runstate.RunFailed {
		t.Fatalf("unaccepted plan completed as %s: %v", row.State, err)
	}
}
