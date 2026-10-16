package store_test

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestFailedRunClosesOwnerAndKeepsVerdict(t *testing.T) {
	st := storetest.OpenSQLite(t)
	tenant, plan, err := storePlanSealRequest(t.Context(), st)
	if err != nil {
		t.Fatal(err)
	}
	const reason = "pipeline missing is not defined in acme/web@abc; defined: build"
	finish := store.FinishRunRequest{Unit: plan.Actor.Owner, Transition: store.RunTransition{RunID: plan.RunID, Actor: plan.Actor, To: store.RunFailed, Cause: store.RunFinish, MaskedError: reason}}
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
	before, err := tenant.ReadSnapshot(t.Context(), finish.Unit.RunID)
	if err != nil {
		t.Fatal(err)
	}
	finish.Transition.To = store.RunSuccess
	if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err == nil {
		t.Fatal("success replaced committed failure")
	}
	after, err := tenant.ReadSnapshot(t.Context(), finish.Unit.RunID)
	before.StoreNow = after.StoreNow
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("conflicting completion changed failed run: %v", err)
	}
}
