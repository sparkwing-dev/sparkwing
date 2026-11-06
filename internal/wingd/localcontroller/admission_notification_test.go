package localcontroller

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestAdmissionCancellationBindingKeepsTheLocalTenant(t *testing.T) {
	var binding Binding
	c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
		binding = b
		seedOwner(t, b, "same-run-id", false)
		if _, err := stateSQL(t, b).ExecContext(t.Context(), `INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`); err != nil {
			return Services{}, err
		}
		foreign, err := b.Store.ForTeam(t.Context(), "foreign")
		if err != nil {
			return Services{}, err
		}
		seedOwner(t, Binding{Home: b.Home, Store: b.Store, Tenant: foreign}, "foreign-run-id", false)
		return serviceFixture(b), nil
	})
	calls := 0
	c.SetAdmissionCancellation(func(context.Context, string) error { calls++; return nil })
	for _, team := range []store.Team{binding.Tenant.Team(), "foreign"} {
		tenant, err := binding.Store.ForTeam(t.Context(), team)
		if err != nil {
			t.Fatal(err)
		}
		runID := "same-run-id"
		if team != binding.Tenant.Team() {
			runID = "foreign-run-id"
		}
		_, err = tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(runID), To: runstate.RunCancelled, Cause: runstate.RunCancel, Actor: runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController}}, Reason: "cancelled"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("local admission received %d tenant cancellations, want 1", calls)
	}
}
