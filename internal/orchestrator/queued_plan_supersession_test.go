package orchestrator

import (
	"context"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type queuedLatestPlan struct {
	sparkwing.Base
	policy sparkwing.OnLimit
	local  bool
}

func (p queuedLatestPlan) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(1))
	plan.Concurrency(sparkwing.NewConcurrencyGroup("latest-checks", sparkwing.ConcurrencyLimit{Capacity: 1, OnLimit: p.policy}))
	if p.local {
		plan.Concurrency(sparkwing.NewConcurrencyGroup("local-checks", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeBox}))
	}
	sparkwing.Job(plan, "check", func(context.Context) error { return nil })
	return nil
}

var registerQueuedLatestPlan sync.Once

func registerQueuedPlanTests() {
	registerQueuedLatestPlan.Do(func() {
		for name, fixture := range map[string]queuedLatestPlan{
			"queued-latest-checks": {policy: sparkwing.CancelOthers},
			"queued-skip-checks":   {policy: sparkwing.Skip},
			"queued-fail-checks":   {policy: sparkwing.Fail},
			"queued-mixed-checks":  {policy: sparkwing.Queue, local: true},
		} {
			sparkwing.Register(name, func() sparkwing.Pipeline[sparkwing.NoInputs] { return fixture })
		}
	})
}

type queuedDepartureObserver struct {
	wingd.RunStore
	departed chan struct{}
}

func (o *queuedDepartureObserver) FinalizeRun(runID string) {
	o.RunStore.FinalizeRun(runID)
	if runID == "older-checks" {
		close(o.departed)
	}
}

func TestQueuedGlobalPlanSupersededBeforeHostAdmission(t *testing.T) {
	registerQueuedPlanTests()
	home := wingdTestHome(t)
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	observer := &queuedDepartureObserver{RunStore: runs, departed: make(chan struct{})}
	startWingdCfg(t, wingd.Config{
		Home: home, Version: "test", Runs: observer,
		Sampler:          stubSampler{wingd.HostStat{TotalCores: 1, TotalMemoryBytes: 64 << 30, FreeMemoryBytes: 64 << 30, LoadMeasured: true, MemoryMeasured: true}},
		HeadroomFraction: -1, GraceWindow: -1,
	})
	backends, st, _ := openWingdBackends(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), wingdTestWait)
	defer cancel()
	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	blocker := acquireWingd(t, cl, wingwire.AdmissionRequest{RunID: "host-blocker", Resources: wingwire.HostResources{Cores: 1}})
	defer blocker.Release()
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	run := func(id string) <-chan *Result {
		done := make(chan *Result, 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, runErr := Run(ctx, backends, Options{Pipeline: "queued-latest-checks", RunID: id, Admission: testWingdAdmission(home, nil)})
			if runErr != nil {
				t.Errorf("run %s: %v", id, runErr)
			}
			done <- result
		}()
		return done
	}
	old := run("older-checks")
	awaitWaiter(t, home, "older-checks")
	newest := run("newest-checks")
	awaitWaiter(t, home, "newest-checks")
	select {
	case result := <-old:
		if result == nil || result.Status != "cancelled" || result.Error == nil || !strings.Contains(result.Error.Error(), "latest-checks") {
			t.Fatalf("superseded result = %+v, want cancellation naming the group", result)
		}
	case <-ctx.Done():
		t.Fatal("superseded run did not cancel while host capacity remained occupied")
	}
	select {
	case <-observer.departed:
	case <-ctx.Done():
		t.Fatal("superseded run did not withdraw from host admission")
	}
	state := queryWingd(t, home)
	if !hasWingdHolder(state, "host-blocker") {
		t.Fatal("host blocker released before cancellation")
	}
	for _, waiter := range state.Waiters {
		if waiter.RunID == "older-checks" {
			t.Fatal("superseded run remains in the host queue")
		}
	}
	nodes, err := st.ListNodes(ctx, "older-checks")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.Status == "success" || node.Status == "running" {
			t.Fatalf("superseded node executed: %+v", node)
		}
	}
	blocker.Release()
	select {
	case result := <-newest:
		if result == nil || result.Status != "success" {
			t.Fatalf("newest result = %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("newest run did not finish after host capacity was released")
	}
}

func TestGlobalPlanRejectionAvoidsHostAdmission(t *testing.T) {
	registerQueuedPlanTests()
	for _, tc := range []struct {
		pipeline string
		kind     store.AcquireKind
		status   string
	}{
		{"queued-skip-checks", store.AcquireSkipped, "success"},
		{"queued-fail-checks", store.AcquireFailed, "failed"},
	} {
		t.Run(tc.pipeline, func(t *testing.T) {
			home := wingdTestHome(t)
			backends, _, _ := openWingdBackends(t, home)
			coordination := &planAcquireFake{kinds: map[string]store.AcquireKind{"g:latest-checks": tc.kind}}
			backends.Concurrency = coordination
			admission := testWingdAdmission(home, nil)
			admission.Spawn = func(string, string) error {
				t.Error("rejected plan requested host admission")
				return wingdclient.ErrNoDaemonHost
			}
			result, err := Run(context.Background(), backends, Options{Pipeline: tc.pipeline, Admission: admission})
			if err != nil || result == nil || result.Status != tc.status {
				t.Fatalf("result=%+v err=%v, want %s", result, err, tc.status)
			}
			if len(coordination.releases) != 0 {
				t.Fatalf("rejected acquisition released a slot: %v", coordination.releases)
			}
		})
	}
}

func TestGlobalPlanReleasesOwnershipAfterHostRefusal(t *testing.T) {
	registerQueuedPlanTests()
	home := wingdTestHome(t)
	backends, _, _ := openWingdBackends(t, home)
	coordination := &planAcquireFake{}
	backends.Concurrency = coordination
	admission := testWingdAdmission(home, nil)
	admission.Spawn = wingdclient.NoHostSpawn
	result, err := Run(context.Background(), backends, Options{Pipeline: "queued-latest-checks", RunID: "refused-checks", Admission: admission})
	if err != nil || result == nil || result.Status != "failed" {
		t.Fatalf("result=%+v err=%v, want host refusal", result, err)
	}
	if want := []string{"g:latest-checks\x00refused-checks/-"}; !reflect.DeepEqual(coordination.releases, want) {
		t.Fatalf("releases=%v, want %v", coordination.releases, want)
	}
}

func TestGlobalPlanFallbackAcquiresLocalGroupsOnce(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name := "standalone"
		if fallback {
			name = "fallback"
		}
		t.Run(name, func(t *testing.T) {
			registerQueuedPlanTests()
			home := wingdTestHome(t)
			backends, _, _ := openWingdBackends(t, home)
			coordination := &planAcquireFake{}
			backends.Concurrency = coordination
			var admission *LocalAdmission
			if fallback {
				admission = unhostedAdmission(home, io.Discard)
			}
			result, err := Run(context.Background(), backends, Options{Pipeline: "queued-mixed-checks", Admission: admission})
			if err != nil || result == nil || result.Status != "success" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if len(coordination.requests) != 2 || coordination.requests[0].Key != "g:latest-checks" || !strings.HasSuffix(coordination.requests[1].Key, "local-checks") {
				t.Fatalf("acquisitions=%+v, want global once then local once", coordination.requests)
			}
			if len(coordination.releases) != 2 {
				t.Fatalf("releases=%v, want both memberships released", coordination.releases)
			}
		})
	}
}
