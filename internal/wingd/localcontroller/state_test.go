package localcontroller

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestStateReconcilesLocalOrphansAndPreservesRemoteWork(t *testing.T) {
	var binding Binding
	c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
		binding = b
		for _, id := range []string{"unstarted", "started", "remote", "live"} {
			seedOwner(t, b, id, id != "unstarted")
		}
		if _, err := stateSQL(t, b).ExecContext(t.Context(), `UPDATE runs SET request_json=? WHERE id='remote'`, []byte(`{"version":1,"request":{"origin":"trigger"}}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := stateSQL(t, b).ExecContext(t.Context(), `UPDATE triggers SET lease_expires_at=? WHERE id='live'`, time.Now().Add(time.Hour).UnixNano()); err != nil {
			t.Fatal(err)
		}
		for _, outcome := range []string{"success", "failed", "cancelled", "skipped", "superseded", "memoized"} {
			if _, err := stateSQL(t, b).ExecContext(t.Context(), `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,spec_json,execution_json)
VALUES (?,'live',?,'done',?,1,?,?)`, b.Tenant.Team(), outcome, outcome, []byte(`{}`), []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
		}
		services := serviceFixture(b)
		var err error
		services.State, err = NewState(b)
		if err != nil {
			t.Fatal(err)
		}
		if err := services.State.Ready(); err == nil {
			t.Fatal("unreconciled state was ready")
		}
		return services, nil
	})
	activity, err := c.Reconcile(t.Context())
	if err != nil || activity.Runs != 3 || activity.QueuedTriggers != 2 || activity.HeldLeases != 1 {
		t.Fatalf("activity: %+v: %v", activity, err)
	}
	if err := c.Ready(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]store.RunState{"started": store.RunFailed, "remote": store.RunRunning, "live": store.RunRunning} {
		run, err := binding.Tenant.ReadRun(t.Context(), id)
		if err != nil || run.State != want {
			t.Fatalf("%s: %+v: %v", id, run, err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.State.Ready(); err == nil {
		t.Fatal("closed store was ready")
	}
}

func FuzzStateActivityExcludesForeignAndPublishedRuns(f *testing.F) {
	f.Add(uint8(1), uint8(2))
	f.Add(uint8(3), uint8(1))
	f.Fuzz(func(t *testing.T, foreign, published uint8) {
		var state *localState
		c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
			seedOwner(t, b, "own", false)
			if _, err := stateSQL(t, b).ExecContext(t.Context(), `INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`); err != nil {
				t.Fatal(err)
			}
			foreignTenant, err := b.Store.ForTeam(t.Context(), "foreign")
			if err != nil {
				t.Fatal(err)
			}
			for i := range int(foreign % 4) {
				seedOwner(t, Binding{Store: b.Store, Tenant: foreignTenant}, string(rune('a'+i)), false)
			}
			for i := range int(published % 4) {
				id := string(rune('k' + i))
				seedOwner(t, b, id, false)
				if _, err := stateSQL(t, b).ExecContext(t.Context(), `UPDATE runs SET intake_mode='publish' WHERE id=?`, id); err != nil {
					t.Fatal(err)
				}
			}
			services := serviceFixture(b)
			services.State, err = NewState(b)
			if err != nil {
				t.Fatal(err)
			}
			state = services.State.(*localState)
			return services, nil
		})
		if _, err := c.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		activity, err := state.snapshot(t.Context())
		if err != nil || activity.Runs != 1 || activity.QueuedTriggers != 1 || activity.HeldLeases != 0 {
			t.Fatalf("foreign=%d published=%d: %+v: %v", foreign, published, activity, err)
		}
	})
}

func TestStateWatchReportsChangesAndPropagatesReporterFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var binding Binding
		var state State
		c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
			binding = b
			services := serviceFixture(b)
			var err error
			state, err = NewState(b)
			services.State = state
			return services, err
		})
		if _, err := c.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		failure := errors.New("report failed")
		var reports int
		err := state.Watch(t.Context(), func(activity wingd.LocalActivity) error {
			reports++
			if reports == 1 {
				if activity != (wingd.LocalActivity{}) {
					t.Fatalf("initial activity: %+v", activity)
				}
				seedOwner(t, binding, "pending", false)
				return nil
			}
			if activity.Runs != 1 || activity.QueuedTriggers != 0 {
				t.Fatalf("updated activity: %+v", activity)
			}
			return failure
		})
		if !errors.Is(err, failure) || reports != 2 {
			t.Fatalf("reports=%d: %v", reports, err)
		}
	})
}
