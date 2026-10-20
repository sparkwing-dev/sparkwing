//go:build unix

package nodehost

import (
	"context"
	"fmt"
	"sync/atomic"
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
	config.Store, config.Owner = fixture.Tenant, ownerOperationsFixture(fixture.Host)
	config.Admission = ComposeNativeAdmission(fixture.Tenant, "", nil, true)
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

func TestNativeHostBindsRealCapacityPerRun(t *testing.T) {
	first := newStoreHostFixture(t, capacityPlan("queue", 0, "a"))
	second := newStoreHostFixtureOn(t, first.Store, capacityPlan("queue", 0, "a"), runspec.Options{})
	config, _, _, _ := nativeHostFixture(t)
	config.Store, config.Owner = first.Tenant, ownerOperationsFixture(first.Host)
	config.Admission = ComposeNativeAdmission(first.Tenant, "", &LocalAdmission{Daemon: newFakeDaemon()}, false)
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

func TestNativeRunnerSchedulesWithConcurrencyAndOptionalCompute(t *testing.T) {
	for _, test := range []struct {
		compute bool
		scope   string
	}{{false, "run"}, {true, "run"}, {false, "box"}, {true, "box"}} {
		compute := test.compute
		t.Run(fmt.Sprintf("local_admission=%t/scope=%s", compute, test.scope), func(t *testing.T) {
			plan := capacityPlan("queue", 0, "a", "b")
			for i := range plan.Jobs {
				plan.Jobs[i].Concurrency[0].Scope = new(test.scope)
				plan.Jobs[i].Resources = &nodeproto.ResourceSpec{Cores: new(0.25), MemoryBytes: new(int64(1024))}
			}
			fixture := newStoreHostFixture(t, plan)
			config, _, _, _ := nativeHostFixture(t)
			config.Store, config.Owner = fixture.Tenant, ownerOperationsFixture(fixture.Host)
			daemon := newFakeDaemon()
			var machine *LocalAdmission
			if compute {
				machine = &LocalAdmission{Daemon: daemon}
			}
			config.Admission = ComposeNativeAdmission(fixture.Tenant, "box-one", machine, false)
			owner, _, _, err := NewNativeHost(config)
			if err != nil {
				t.Fatal(err)
			}
			var executed atomic.Int32
			key := scopedConcurrencyKey("db", test.scope, fixture.Accepted.RunID, "box-one")
			owner.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				executed.Add(1)
				state, err := fixture.Tenant.GetConcurrencyState(ctx, key)
				if err != nil || state == nil || len(state.Holders) != 1 || state.Holders[0].NodeID != input.NodeID || state.UsedCost != 1 {
					t.Errorf("runner lost logical concurrency: %+v %v", state, err)
				}
				return fixture.report(ctx, input, store.NodeSuccess)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			completion, err := owner.Run(ctx, fixture.Input)
			if err != nil || executed.Load() != 2 || completion.Outcome != "success" {
				t.Fatalf("completion=%+v executed=%d error=%v", completion, executed.Load(), err)
			}
			state, err := fixture.Tenant.GetConcurrencyState(ctx, key)
			if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 0 {
				t.Fatalf("concurrency leaked: %+v %v", state, err)
			}
			if live, granted, released := daemon.counts(); live != 0 || granted != released || (granted > 0) != compute {
				t.Fatalf("machine admission live/granted/released=%d/%d/%d", live, granted, released)
			}
			computeRequests := 0
			for _, request := range daemon.requests() {
				if request.Resources.Cores > 0 || request.Resources.MemoryBytes > 0 {
					computeRequests++
					if request.Resources.Cores != 0.25 || request.Resources.MemoryBytes != 1024 {
						t.Errorf("machine admission changed resource hints: %+v", request)
					}
				}
				if len(request.Semaphores) != 0 {
					t.Errorf("machine admission received controller concurrency: %+v", request)
				}
			}
			if compute && computeRequests != 2 {
				t.Fatalf("compute requests=%d, want one per job", computeRequests)
			}
		})
	}
}

func TestNativeClaimedComputeDoesNotReacquireConcurrency(t *testing.T) {
	capacity, probe, accepted, row := capacityFixture(t)
	daemon := newFakeDaemon()
	config, _, _, _ := nativeHostFixture(t)
	config.Admission = ComposeNativeAdmission(probe, "", &LocalAdmission{Daemon: daemon}, false)
	_, claimed, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := capacity.Node(t.Context(), accepted, row, func(ctx context.Context) error {
		return claimed.Admission.ClaimedNode(ctx, accepted, row, func(context.Context) error { return nil })
	}); err != nil {
		t.Fatal(err)
	}
	requests := daemon.requests()
	if len(requests) != 1 || len(requests[0].Semaphores) != 0 || len(probe.requests) != 1 {
		t.Fatalf("claimed compute reacquired concurrency: machine=%+v store=%+v", requests, probe.requests)
	}
}
