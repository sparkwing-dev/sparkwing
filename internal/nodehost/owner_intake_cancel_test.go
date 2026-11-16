package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

type intakeCancelSnapshot struct {
	OwnerStore
	cancel func()
}

func (s intakeCancelSnapshot) ReadSnapshot(ctx context.Context, id string) (store.RunSnapshot, error) {
	s.cancel()
	return s.OwnerStore.ReadSnapshot(ctx, id)
}

type intakeCancelAcceptance struct{ intakeCancelSnapshot }

func (s intakeCancelAcceptance) ReadSnapshot(ctx context.Context, id string) (store.RunSnapshot, error) {
	return s.OwnerStore.ReadSnapshot(ctx, id)
}

func (s intakeCancelAcceptance) AcceptPlan(ctx context.Context, request store.PlanAcceptance) (store.AcceptedPlan, error) {
	s.cancel()
	return s.OwnerStore.AcceptPlan(ctx, request)
}

func TestOwnerCancellationBeforeAcceptanceFinishesStartedTrigger(t *testing.T) {
	for _, stage := range []string{"renewal", "snapshot", "acceptance"} {
		t.Run(stage, func(t *testing.T) {
			database := openStore(t)
			holder := newStoreHostFixtureOn(t, database, nodewire.PlanDoc{Concurrency: []nodewire.ConcurrencySpec{{Group: "shared", Scope: new("global"), Capacity: new(1), OnLimit: new("queue")}}, Jobs: []nodewire.JobSpec{{ID: "held", Steps: []nodewire.StepSpec{{ID: "body"}}}}}, runspec.Options{})
			if got, err := holder.Tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: holder.Input.RunID, Generation: int64(holder.Actor.Owner.Generation)}, holder.Actor); err != nil || got.Kind != runstate.AcquireGranted {
				t.Fatalf("holder admission: %+v %v", got, err)
			}
			holderRun, err := holder.Tenant.ReadRun(t.Context(), holder.Input.RunID)
			if err != nil {
				t.Fatal(err)
			}
			f := newStoreOwnerFixture(t, database, holderRun.Request.Request)
			if _, err := f.Host.Renew(t.Context(), f.Actor, true); err != nil {
				t.Fatal(err)
			}
			var started int64
			if err := teststore.SQL(t, database).QueryRowContext(t.Context(), `SELECT started_at FROM triggers WHERE id=? AND status='claimed'`, f.Input.RunID).Scan(&started); err != nil || started == 0 {
				t.Fatalf("started trigger: %d %v", started, err)
			}
			f.Host.Acquire = func(context.Context, RunInput) (*source.Entry, error) {
				if stage != "acceptance" {
					t.Error("cancelled intake acquired source")
				}
				return &source.Entry{Engine: engine.Resolved{Identity: holderRun.Spec.Resolved.Engine}}, nil
			}
			cancel := func() {
				if _, err := f.Tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(f.Input.RunID), Actor: f.Control, To: runstate.RunCancelled, Cause: runstate.RunCancel, Reason: "user_cancelled"}); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "renewal" {
				cancel()
			} else if stage == "snapshot" {
				f.Host.Store = intakeCancelSnapshot{OwnerStore: f.Host.Store, cancel: cancel}
			} else {
				f.Host.Store = intakeCancelAcceptance{intakeCancelSnapshot{OwnerStore: f.Host.Store, cancel: cancel}}
				f.Host.Prepare = func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) {
					return OwnerPlanning{Planning: PlanningInput{Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal}}, Plan: func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{"jobs":[]}`), nil }}, nil
				}
			}
			_, err = f.Host.Run(t.Context(), f.Input)
			var cancelled *lease.CancelError
			if !errors.As(err, &cancelled) {
				t.Errorf("cancellation cause: %v", err)
			}
			if _, err := database.AsOperator().ReapUnits(t.Context(), 10); err != nil {
				t.Fatal(err)
			}
			run, err := f.Tenant.ReadRun(t.Context(), f.Input.RunID)
			if err != nil || run.State != runstate.RunCancelled || run.FinishedAt == nil || run.Spec != nil {
				t.Fatalf("cancelled started trigger stayed %s (intent=%v finished=%v): %v", run.State, run.CancelRequestedAt, run.FinishedAt, err)
			}
			var executable int
			if err := teststore.SQL(t, database).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM triggers WHERE id=? AND status<>'done'`, f.Input.RunID).Scan(&executable); err != nil || executable != 0 {
				t.Fatalf("cancelled trigger retained execution: %d %v", executable, err)
			}
			if _, err := f.Tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: f.Input.RunID, Generation: int64(f.Actor.Owner.Generation)}, f.Actor); err == nil {
				t.Fatal("cancelled trigger acquired capacity")
			}
			for _, table := range []string{"nodes", "node_attempts", "concurrency_waiters"} {
				var count int
				if err := teststore.SQL(t, database).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table+` WHERE run_id=?`, f.Input.RunID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("cancelled intake created %s: %d %v", table, count, err)
				}
			}
			state, err := holder.Tenant.GetConcurrencyState(t.Context(), "plan:global:shared")
			if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != holder.Input.RunID || state.UsedCost != 1 {
				t.Fatalf("cancellation released holder: %+v %v", state, err)
			}
		})
	}
}
