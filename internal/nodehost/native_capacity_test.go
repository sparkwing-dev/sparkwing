//go:build unix

package nodehost

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNativeHostCapacityStoreCannotReplaceMachineAdmission(t *testing.T) {
	fixture := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
	config, _, _, _ := nativeHostFixture(t)
	config.Store, config.Owner = fixture.Tenant, fixture.Host
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	executed := false
	owner.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		executed = true
		return fixture.report(ctx, input, store.NodeSuccess)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := owner.Run(ctx, fixture.Input)
	if err == nil || executed || completion.Outcome != "" {
		t.Fatalf("completion=%+v executed=%v error=%v", completion, executed, err)
	}
	row, err := fixture.Tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: fixture.Accepted.RunID, NodeID: "job"})
	if err != nil || row.Status != store.StatusPending || row.ReadyAt != nil || row.ClaimedBy != "" {
		t.Fatalf("released without machine admission: row=%+v error=%v", row, err)
	}
}

func TestNativeHostAutomaticallyBindsRealCapacityPerRun(t *testing.T) {
	first := newStoreHostFixture(t, capacityPlan("queue", 0, "a"))
	second := newStoreHostFixtureOn(t, first.Store, capacityPlan("queue", 0, "a"), runspec.Options{})
	config, _, _, _ := nativeHostFixture(t)
	config.Store, config.Owner = first.Tenant, first.Host
	config.Admission = &LocalAdmission{Daemon: newFakeDaemon()}
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []*storeHostFixture{first, second} {
		owner.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
			state, err := fixture.Tenant.GetConcurrencyState(ctx, "global:db")
			if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != fixture.Accepted.RunID || state.Holders[0].NodeID != "a" {
				t.Errorf("executed without native store capacity: state=%+v err=%v", state, err)
			}
			return fixture.report(ctx, input, store.NodeSuccess)
		})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		completion, err := owner.Run(ctx, fixture.Input)
		cancel()
		if err != nil || completion.Outcome != "success" {
			t.Fatalf("native completion=%+v err=%v", completion, err)
		}
		state, err := fixture.Tenant.GetConcurrencyState(t.Context(), "global:db")
		if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 0 {
			t.Fatalf("native capacity leaked: state=%+v err=%v", state, err)
		}
	}
}

func TestNativeHostRejectsGroupsWithoutCapacityStore(t *testing.T) {
	config, _, _, _ := nativeHostFixture(t)
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	_, _, accepted, row := capacityFixture(t)
	if err := owner.Admit(t.Context(), accepted, row, func(context.Context) error { t.Error("executed without store"); return nil }); err == nil {
		t.Fatal("missing store admitted job group")
	}
	accepted.Plan = planGrouped("global", "queue", "a")
	fixture := newStoreHostFixture(t, accepted.Plan)
	if _, err := owner.Around(t.Context(), fixture.Accepted, func(context.Context) (RunCompletion, error) {
		t.Error("started plan without store")
		return RunCompletion{}, nil
	}); err == nil {
		t.Fatal("missing store admitted plan group")
	}
}
