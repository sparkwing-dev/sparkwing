package controller

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func controllerCapacityFixture(t *testing.T, opts ...store.Option) (*serverFixture, *store.Tenant, capability.Principal) {
	t.Helper()
	engine, tenant := engineRuntimeStore(t, opts...)
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Dispatch = runspec.DispatchController
	request, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, engine.Store, `UPDATE runs SET dispatch='controller',request_json=? WHERE id='run'`, request)
	engineSQL(t, engine.Store, `UPDATE triggers SET unit_kind='plan' WHERE id='run'`)
	owner := engineRuntimeClaim(t, engine, capability.UnitPlan)
	server := New(engine.Store, nil)
	return &serverFixture{Server: server, store: engine.Store, engine: engine}, tenant, owner.Principal
}

func TestControllerPlanAdmissionRefusalFinishesUnreleasedWork(t *testing.T) {
	for _, test := range []struct {
		policy string
		run    runstate.RunState
		node   runstate.NodeState
	}{
		{"skip", runstate.RunSuccess, runstate.NodeSkipped},
		{"fail", runstate.RunFailed, runstate.NodeFailed},
		{"queue", runstate.RunFailed, runstate.NodeFailed},
	} {
		t.Run(test.policy, func(t *testing.T) {
			now := time.Now()
			server, tenant, owner := controllerCapacityFixture(t, teststore.WithClock(func() time.Time { return now }))
			seedOtherCapacityHolder(t, server, "plan:global:serial")
			group := nodeproto.ConcurrencySpec{Group: "serial", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: &test.policy}
			if test.policy == "queue" {
				group.QueueTimeoutMS = new(int64(1))
			}
			acceptCapacityDocument(t, server, tenant, owner, nodeproto.PlanDoc{
				Concurrency: []nodeproto.ConcurrencySpec{group},
				Jobs:        []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}},
			})
			if test.policy == "queue" {
				row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "build"})
				if err != nil || row.ReadyAt != nil || row.Status != runstate.StatusPending {
					t.Fatalf("queue released job before capacity: %+v %v", row, err)
				}
				now = now.Add(time.Second)
				server.reconcileOwnedRuns(t.Context())
			}
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || run.State != test.run || run.FinishedAt == nil {
				t.Fatalf("controller admission policy did not finish run: %+v %v", run, err)
			}
			row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "build"})
			if err != nil || row.Status != runstate.StatusDone || row.Outcome != test.node || row.ReadyAt != nil || row.Attempt != 0 {
				t.Fatalf("controller policy released refused job: %+v %v", row, err)
			}
			if test.policy == "queue" {
				events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 10)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, event := range events {
					if event.NodeID == "build" {
						var result runstate.NodeResult
						if err := json.Unmarshal(event.Payload, &result); err != nil || result.Reason != store.FailureQueueTimeout {
							t.Fatalf("queue timeout lost policy reason: %+v %v", result, err)
						}
						found = true
					}
				}
				if !found {
					t.Fatal("queue timeout lost job result event")
				}
			}
			state, err := tenant.GetConcurrencyState(t.Context(), "plan:global:serial")
			if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != "other" || len(state.Waiters) != 0 {
				t.Fatalf("refused run changed occupied capacity: %+v %v", state, err)
			}
		})
	}
}

func TestPrivateCapacityEvalUsesOnlyItsAwardedSource(t *testing.T) {
	server, tenant, owner := controllerCapacityFixture(t)
	acceptCapacityDocument(t, server, tenant, owner, nodeproto.PlanDoc{
		Concurrency: []nodeproto.ConcurrencySpec{{Group: "serial", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")}},
		Jobs: []nodeproto.JobSpec{
			{
				ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}, Closures: nodeproto.ClosureSpec{Generate: []string{"generated"}},
				Concurrency: []nodeproto.ConcurrencySpec{{Group: "job-only", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")}},
			},
			{ID: "another-source", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		},
	})
	claim := func(unit store.UnitRef) store.UnitClaim {
		t.Helper()
		claimed, err := server.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &unit})
		if err != nil {
			t.Fatal(err)
		}
		return claimed
	}
	source := claim(store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "build"})
	principal, err := server.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(source.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	actor := engineActor(capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "build"}})
	ref := runspec.NodeRef{RunID: "run", NodeID: "build"}
	if result, err := tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: "run", NodeID: "build", Generation: int64(source.Award.Generation)}, actor); err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("source admission: %+v %v", result, err)
	}
	started, err := tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(0))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionNode(t.Context(), store.ReportNode{Ref: ref, Actor: actor, Outcome: runstate.NodeSuccess, SourceAttempt: &started.Attempt, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}}); err != nil {
		t.Fatal(err)
	}
	eval := claim(store.UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "build", SourceGeneration: int64(source.Award.Generation)})
	binding := runstate.CapacityBinding{RunID: "run", NodeID: "build", Generation: int64(source.Award.Generation)}
	if code := privateCapacityRequest(t, server, string(eval.Bearer), localhostwire.LocalCapacityRequest{Binding: binding}); code != http.StatusForbidden && code != http.StatusConflict {
		t.Fatalf("eval award admitted source capacity: HTTP %d", code)
	}
	for _, wrong := range []runstate.CapacityBinding{
		{RunID: "run", NodeID: "another-source", Generation: binding.Generation},
		{RunID: "run", NodeID: binding.NodeID, Generation: binding.Generation + 1},
		{RunID: "run", Generation: binding.Generation},
		{RunID: "foreign", NodeID: binding.NodeID, Generation: binding.Generation},
	} {
		if code := privateCapacityRequest(t, server, string(eval.Bearer), localhostwire.LocalCapacityRequest{Binding: wrong}); code != http.StatusForbidden && code != http.StatusConflict {
			t.Fatalf("eval widened source binding to %+v: HTTP %d", wrong, code)
		}
	}
	planState, err := tenant.GetConcurrencyState(t.Context(), "plan:global:serial")
	if err != nil || len(planState.Holders) != 1 || planState.Holders[0].NodeID != "" || planState.UsedCost != 1 {
		t.Fatalf("eval duplicated run capacity: %+v %v", planState, err)
	}
	jobState, err := tenant.GetConcurrencyState(t.Context(), "job:global:job-only")
	if err != nil || len(jobState.Holders) != 0 || len(jobState.Waiters) != 0 {
		t.Fatalf("eval revived source job capacity: %+v %v", jobState, err)
	}
}

func TestControllerReleaseAcquiresPlanCapacityBeforeReleasingJobs(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(map[bool]string{false: "available", true: "occupied"}[occupied], func(t *testing.T) {
			server, tenant, owner := controllerCapacityFixture(t)
			if occupied {
				seedOtherCapacityHolder(t, server, "plan:global:serial")
			}
			acceptCapacityDocument(t, server, tenant, owner, nodeproto.PlanDoc{
				Concurrency: []nodeproto.ConcurrencySpec{{Group: "serial", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new("queue")}},
				Jobs:        []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}},
			})
			row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "build"})
			if err != nil || (row.ReadyAt == nil) != occupied {
				t.Fatalf("release escaped plan admission: node=%+v occupied=%v error=%v", row, occupied, err)
			}
			state, err := tenant.GetConcurrencyState(t.Context(), "plan:global:serial")
			if err != nil || len(state.Holders) != 1 {
				t.Fatalf("plan capacity: %+v %v", state, err)
			}
			if occupied {
				if len(state.Waiters) != 1 || state.Waiters[0].RunID != "run" || state.Waiters[0].NodeID != "" {
					t.Fatalf("run did not wait before release: %+v", state)
				}
				engineSQL(t, server.store, `UPDATE runs SET status='success',finished_at=? WHERE team='a' AND id='other'`, time.Now().UnixNano())
				if _, err := server.store.AsOperator().MaintainConcurrency(t.Context()); err != nil {
					t.Fatal(err)
				}
				server.reconcileOwnedRuns(t.Context())
				row, err = tenant.ReadNode(t.Context(), row.Ref)
				if err != nil || row.ReadyAt == nil {
					t.Fatalf("controller did not resume after capacity release: %+v %v", row, err)
				}
				state, err = tenant.GetConcurrencyState(t.Context(), "plan:global:serial")
			}
			if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != "run" || state.Holders[0].NodeID != "" || state.UsedCost != 1 || len(state.Waiters) != 0 {
				t.Fatalf("controller did not hold exactly one run reservation: %+v %v", state, err)
			}
			before := state.Holders[0]
			var previousGeneration int64
			if err := teststore.SQL(t, server.store).QueryRowContext(t.Context(), `SELECT generation FROM concurrency_holders WHERE team='a' AND key='plan:global:serial' AND run_id='run' AND node_id=''`).Scan(&previousGeneration); err != nil {
				t.Fatal(err)
			}
			successor := New(server.store, nil)
			successor.reconcileOwnedRuns(t.Context())
			successor.reconcileOwnedRuns(t.Context())
			state, err = tenant.GetConcurrencyState(t.Context(), "plan:global:serial")
			run, runErr := tenant.ReadRun(t.Context(), "run")
			var generation int64
			if err := teststore.SQL(t, server.store).QueryRowContext(t.Context(), `SELECT generation FROM concurrency_holders WHERE team='a' AND key='plan:global:serial' AND run_id='run' AND node_id=''`).Scan(&generation); err != nil {
				t.Fatal(err)
			}
			if err != nil || runErr != nil || run.Owner == nil || len(state.Holders) != 1 || state.UsedCost != 1 || !state.Holders[0].ClaimedAt.Equal(before.ClaimedAt) || generation != previousGeneration || !state.Holders[0].QueueArrivedAt.Equal(before.QueueArrivedAt) {
				t.Fatalf("controller recovery reacquired capacity: before=%+v state=%+v owner=%+v errors=%v/%v", before, state, run.Owner, err, runErr)
			}
		})
	}
}
