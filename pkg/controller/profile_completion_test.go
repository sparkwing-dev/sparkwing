package controller

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func preparedProfileRun(t *testing.T, opts ...store.Option) (*Server, *store.Tenant, func(store.RunState)) {
	t.Helper()
	server, host, tenant, owner := localHostFixture(t, opts...)
	start := time.Now().Add(-time.Minute)
	if err := teststore.SeedNode(server.store, t.Context(), store.Node{RunID: "run", NodeID: "build", Status: "running", StartedAt: &start}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNodeFinish(server.store, t.Context(), tenant.Team(), store.NodeRef{RunID: "run", NodeID: "build"}, store.NodeResult{Outcome: store.NodeSuccess}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		engineSQL(t, server.store, `INSERT INTO node_metrics(team,run_id,node_id,ts,cpu_millicores,memory_bytes,cpu_time_nanos,kind) VALUES ('a','run','build',?,1250,2048,0,'interval')`, start.Add(time.Duration(i)*time.Second).UnixNano())
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(runspec.Spec{Version: 1, Request: run.Request, Resolved: runspec.Resolved{Guards: runspec.Guards{Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.store, `UPDATE runs SET spec_json=?,spec_hash='spec',sealed_at=1,plan_json=?,plan_hash='plan' WHERE team='a' AND id='run'`, spec, []byte(`{"jobs":[{"id":"build"}]}`))
	actor := engineActor(capability.Authorized{Principal: owner, Binding: owner.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}})
	finish := func(state store.RunState) {
		t.Helper()
		if state == store.RunCancelled {
			engineSQL(t, server.store, `UPDATE runs SET cancel_requested_at=1 WHERE team='a' AND id='run'`)
		}
		if state != store.RunSuccess {
			if err := teststore.SeedNodeFinish(server.store, t.Context(), tenant.Team(), store.NodeRef{RunID: "run", NodeID: "build"}, store.NodeResult{Outcome: store.NodeState(state)}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := host.FinishRunAndUnit(t.Context(), store.FinishRunRequest{Unit: actor.Owner, Transition: store.RunTransition{RunID: "run", Actor: actor, To: state, Cause: store.RunFinish}}); err != nil {
			t.Fatal(err)
		}
	}
	return server, tenant, finish
}

func completedProfileRun(t *testing.T, opts ...store.Option) (*Server, *store.Tenant) {
	t.Helper()
	server, tenant, finish := preparedProfileRun(t, opts...)
	finish(store.RunSuccess)
	for node := range map[string]bool{"": true, "build": true} {
		if p, err := tenant.GetPipelineProfile(t.Context(), "ci", node); err != nil || p != nil {
			t.Fatalf("profile %q folded before delivery: %+v: %v", node, p, err)
		}
	}
	return server, tenant
}

func TestProfileCompletionFromRealTerminalTransition(t *testing.T) {
	server, tenant := completedProfileRun(t)
	for range 3 {
		if err := server.ReconcileRunEffects(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for node := range map[string]bool{"": true, "build": true} {
		p, err := tenant.GetPipelineProfile(t.Context(), "ci", node)
		if err != nil || p == nil || p.SampleCount != 1 || p.PeakCores != 1.25 || p.PeakMemoryBytes != 2048 {
			t.Fatalf("profile %q=%+v: %v", node, p, err)
		}
	}
	var desired, delivered int
	if err := teststore.SQL(t, server.store).QueryRow(`SELECT desired_rev,delivered_rev FROM run_effects WHERE team='a' AND run_id='run' AND effect='profile_fold'`).Scan(&desired, &delivered); err != nil || desired != delivered || desired != 1 {
		t.Fatalf("delivery revision=%d/%d: %v", delivered, desired, err)
	}
}

func TestProfileCompletionRetriesCorruptMetricsWithoutPartialFold(t *testing.T) {
	clock := &testClock{}
	server, tenant := completedProfileRun(t, clock.option())
	now := time.Now()
	clock.pin(func() time.Time { return now })
	engineSQL(t, server.store, `UPDATE node_metrics SET memory_bytes='corrupt' WHERE team='a' AND run_id='run' AND node_id='build'`)
	if err := server.ReconcileRunEffects(t.Context()); err != nil {
		t.Fatal(err)
	}
	var attempts, delivered, next int64
	if err := teststore.SQL(t, server.store).QueryRow(`SELECT attempts,delivered_rev,next_at FROM run_effects WHERE team='a' AND run_id='run' AND effect='profile_fold'`).Scan(&attempts, &delivered, &next); err != nil || attempts != 1 || delivered != 0 || next <= now.UnixNano() {
		t.Fatalf("failed delivery=%d/%d next=%d: %v", attempts, delivered, next, err)
	}
	if p, err := tenant.GetPipelineProfile(t.Context(), "ci", "build"); err != nil || p != nil {
		t.Fatalf("failed read folded partial data: %+v: %v", p, err)
	}
	engineSQL(t, server.store, `UPDATE node_metrics SET memory_bytes=2048 WHERE team='a' AND run_id='run' AND node_id='build'`)
	now = now.Add(time.Minute)
	for range 2 {
		if err := server.ReconcileRunEffects(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	p, err := tenant.GetPipelineProfile(t.Context(), "ci", "build")
	if err != nil || p == nil || p.SampleCount != 1 || p.PeakMemoryBytes != 2048 {
		t.Fatalf("repaired delivery=%+v: %v", p, err)
	}
}
