//go:build unix

package nodehost

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNativeHostCapacityStoreCannotReplaceMachineAdmission(t *testing.T) {
	fixture := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
	config, _, _, _ := nativeHostFixture(t)
	config.Store, config.Owner = fixture.Tenant, ownerOperationsFixture(fixture.Host)
	config.Admission = ComposeStartGate(fixture.Tenant, nil, true)
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	executed := false
	owner.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		executed = true
		return fixture.report(ctx, input, runstate.NodeSuccess)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := owner.Run(ctx, fixture.Input)
	if err != nil || executed || completion.Outcome != "failed" {
		t.Fatalf("completion=%+v executed=%v error=%v", completion, executed, err)
	}
	row, err := fixture.Tenant.ReadNode(ctx, runspec.NodeRef{RunID: runspec.RunID(fixture.Accepted.RunID), NodeID: "job"})
	if err != nil || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeFailed || row.ClaimedBy != "" {
		t.Fatalf("failed machine requirement did not settle: row=%+v error=%v", row, err)
	}
}

func TestNativeHostBindsRealCapacityPerRun(t *testing.T) {
	first := newStoreHostFixture(t, capacityPlan("queue", 0, "a"))
	second := newStoreHostFixtureOn(t, first.Store, capacityPlan("queue", 0, "a"), runspec.Options{})
	config, _, _, _ := nativeHostFixture(t)
	config.Store, config.Owner = first.Tenant, ownerOperationsFixture(first.Host)
	config.Admission = ComposeStartGate(first.Tenant, &LocalAdmission{Daemon: newFakeDaemon()}, false)
	owner, _, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []*storeHostFixture{first, second} {
		owner.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
			state, err := fixture.Tenant.GetConcurrencyState(ctx, "job:global:db")
			if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != fixture.Accepted.RunID || state.Holders[0].NodeID != "a" {
				t.Errorf("executed without native store capacity: state=%+v err=%v", state, err)
			}
			return fixture.report(ctx, input, runstate.NodeSuccess)
		})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		completion, err := owner.Run(ctx, fixture.Input)
		cancel()
		if err != nil || completion.Outcome != "success" {
			t.Fatalf("native completion=%+v err=%v", completion, err)
		}
		state, err := fixture.Tenant.GetConcurrencyState(t.Context(), "job:global:db")
		if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 0 {
			t.Fatalf("native capacity leaked: state=%+v err=%v", state, err)
		}
	}
}

func TestNativeHostRejectsStartsWithoutStore(t *testing.T) {
	_, _, accepted, row := capacityFixture(t)
	gate := ComposeStartGate(nil, nil, false)
	if err := gate.claimedNode(t.Context(), NodeInput{Accepted: accepted, NodeID: string(row.Ref.NodeID), Generation: row.Generation}, func(context.Context) error { t.Fatal("executed without start boundary"); return nil }); err == nil {
		t.Fatal("missing store admitted work")
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
			}
			fixture := newStoreHostFixture(t, plan)
			config, _, _, _ := nativeHostFixture(t)
			config.Store, config.Owner = fixture.Tenant, ownerOperationsFixture(fixture.Host)
			daemon := newFakeDaemon()
			var machine *LocalAdmission
			if compute {
				machine = &LocalAdmission{Daemon: daemon}
			}
			config.Admission = ComposeStartGate(fixture.Tenant, machine, false)
			queued := make(chan struct{}, 1)
			config.Admission.Queued = func(context.Context, runspec.NodeRef, runstate.AcquireSlotResponse) error {
				queued <- struct{}{}
				return nil
			}
			owner, _, _, err := NewNativeHost(config)
			if err != nil {
				t.Fatal(err)
			}
			var executed atomic.Int32
			started, release := make(chan string, 2), make(chan struct{})
			key := fixtureJobCapacityKey("db", test.scope, fixture.Accepted.RunID, fixture.Actor.Owner.Holder)
			owner.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				position := executed.Add(1)
				state, err := fixture.Tenant.GetConcurrencyState(ctx, key)
				if err != nil || state == nil || len(state.Holders) != 1 || state.Holders[0].NodeID != input.NodeID || state.UsedCost != 1 {
					t.Errorf("runner lost logical concurrency: %+v %v", state, err)
				}
				started <- input.NodeID
				if position == 1 {
					select {
					case <-release:
					case <-ctx.Done():
						return context.Cause(ctx)
					}
				}
				return fixture.report(ctx, input, runstate.NodeSuccess)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			var workers sync.WaitGroup
			defer func() { cancel(); workers.Wait() }()
			done := make(chan error, 1)
			workers.Go(func() {
				completion, err := owner.Run(ctx, fixture.Input)
				if err == nil && completion.Outcome != "success" {
					err = fmt.Errorf("runner completion=%+v", completion)
				}
				done <- err
			})
			var first string
			select {
			case first = <-started:
			case result := <-done:
				t.Fatalf("runner ended before execution: %+v", result)
			case <-ctx.Done():
				t.Fatal(context.Cause(ctx))
			}
			select {
			case <-queued:
			case id := <-started:
				t.Fatalf("runner jobs %s and %s overlapped", first, id)
			case result := <-done:
				t.Fatalf("runner ended before contention: %+v", result)
			case <-ctx.Done():
				t.Fatal(context.Cause(ctx))
			}
			state, err := fixture.Tenant.GetConcurrencyState(ctx, key)
			if err != nil || state == nil || len(state.Holders) != 1 || len(state.Waiters) != 1 || state.Holders[0].NodeID != first || state.Waiters[0].NodeID == first || executed.Load() != 1 {
				t.Fatalf("runner did not retain holder and waiter: %+v error=%v executed=%d", state, err, executed.Load())
			}
			wantCompute := 0
			if compute {
				wantCompute = 1
			}
			if live, granted, released := daemon.counts(); live != wantCompute || granted != wantCompute || released != 0 {
				t.Fatalf("queued job acquired compute: live/granted/released=%d/%d/%d", live, granted, released)
			}
			close(release)
			if err := <-done; err != nil || executed.Load() != 2 {
				t.Fatalf("executed=%d error=%v", executed.Load(), err)
			}
			state, err = fixture.Tenant.GetConcurrencyState(ctx, key)
			if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 0 {
				t.Fatalf("concurrency leaked: %+v %v", state, err)
			}
			if live, granted, released := daemon.counts(); live != 0 || granted != released || (granted > 0) != compute {
				t.Fatalf("machine admission live/granted/released=%d/%d/%d", live, granted, released)
			}
			computeRequests := 0
			for _, request := range daemon.requests() {
				computeRequests++
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

func TestNativeClaimedAdmissionAcquiresConcurrencyOnce(t *testing.T) {
	_, probe, accepted, row := capacityFixture(t)
	daemon := newFakeDaemon()
	config, _, _, _ := nativeHostFixture(t)
	config.Admission = ComposeStartGate(probe, &LocalAdmission{Daemon: daemon}, false)
	_, claimed, _, err := NewNativeHost(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := claimed.Admission.claimedNode(t.Context(), NodeInput{Accepted: accepted, NodeID: string(row.Ref.NodeID), Generation: row.Generation, Execution: row.Execution}, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	requests := daemon.requests()
	if len(requests) != 1 || len(requests[0].Semaphores) != 0 || len(probe.bindings) != 1 {
		t.Fatalf("claimed compute reacquired concurrency: machine=%+v store=%+v", requests, probe.bindings)
	}
}

func TestNativeClaimedJobsContendWithOptionalCompute(t *testing.T) {
	for _, compute := range []bool{false, true} {
		t.Run(fmt.Sprintf("local_admission=%t", compute), func(t *testing.T) {
			plan := capacityPlan("queue", 0, "a", "b")
			for i := range plan.Jobs {
				plan.Jobs[i].Concurrency[0].Scope = new("run")
			}
			fixture := newStoreHostFixtureForRequest(t, openStore(t), plan, runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}, Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: "https://example.com/acme/repo.git", PipelineDir: ".sparkwing"}}})
			config, _, _, _ := nativeHostFixture(t)
			config.Store = fixture.Tenant
			daemon := newFakeDaemon()
			var machine *LocalAdmission
			if compute {
				machine = &LocalAdmission{Daemon: daemon}
			}
			config.Admission = ComposeStartGate(fixture.Tenant, machine, false)
			queued := make(chan struct{}, 1)
			config.Admission.Queued = func(_ context.Context, ref runspec.NodeRef, _ runstate.AcquireSlotResponse) error {
				if ref.NodeID == "b" {
					queued <- struct{}{}
				}
				return nil
			}
			_, claimed, _, err := NewNativeHost(config)
			if err != nil {
				t.Fatal(err)
			}
			key := fixtureJobCapacityKey("db", "run", fixture.Accepted.RunID, "")
			claimed.Acquire = func(ctx context.Context, spec source.Spec, _ source.Options) (*source.Entry, error) {
				state, err := fixture.Tenant.GetConcurrencyState(ctx, key)
				if err != nil || state.UsedCost != 1 || len(state.Holders) != 1 {
					return nil, fmt.Errorf("source outside concurrency: %+v %v", state, err)
				}
				return &source.Entry{Engine: engine.Resolved{Identity: *spec.Engine}, SHA: spec.SHA, RepoKey: spec.RepoKey}, nil
			}
			started, release := make(chan string, 2), make(chan struct{})
			var active atomic.Int32
			claimed.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				if active.Add(1) != 1 {
					t.Error("claimed jobs overlapped")
				}
				started <- input.NodeID
				if input.NodeID == "a" {
					select {
					case <-release:
					case <-ctx.Done():
						return context.Cause(ctx)
					}
				}
				active.Add(-1)
				return fixture.report(ctx, input, runstate.NodeSuccess)
			})
			inputs := make([]NodeInput, 0, 2)
			for _, id := range []string{"a", "b"} {
				releaseRemote(t, fixture, id)
				_, input := claimRemote(t, fixture, id)
				inputs = append(inputs, input)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			var workers sync.WaitGroup
			defer func() { cancel(); workers.Wait() }()
			done := make(chan error, 2)
			workers.Go(func() { done <- claimed.Execute(ctx, inputs[0]) })
			select {
			case id := <-started:
				if id != "a" {
					t.Fatal(id)
				}
			case err := <-done:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal(context.Cause(ctx))
			}
			workers.Go(func() { done <- claimed.Execute(ctx, inputs[1]) })
			select {
			case <-queued:
			case id := <-started:
				t.Fatalf("job %s bypassed concurrency", id)
			case err := <-done:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal(context.Cause(ctx))
			}
			state, err := fixture.Tenant.GetConcurrencyState(ctx, key)
			if err != nil || len(state.Holders) != 1 || len(state.Waiters) != 1 || state.Holders[0].NodeID != "a" || state.Waiters[0].NodeID != "b" {
				t.Fatalf("contention=%+v %v", state, err)
			}
			close(release)
			for range 2 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			if id := <-started; id != "b" {
				t.Fatal(id)
			}
			state, err = fixture.Tenant.GetConcurrencyState(ctx, key)
			if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 0 {
				t.Fatalf("capacity leaked: %+v %v", state, err)
			}
			live, granted, released := daemon.counts()
			want := 0
			if compute {
				want = 2
			}
			if live != 0 || granted != want || released != want {
				t.Fatalf("compute=%d/%d/%d", live, granted, released)
			}
			for _, request := range daemon.requests() {
				if len(request.Semaphores) != 0 {
					t.Fatalf("duplicate concurrency: %+v", request)
				}
			}
		})
	}
}
