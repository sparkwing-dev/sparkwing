package controller

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestControllerPlanContinuityLapseWaitsWithoutStoppingReleasedWork(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.1s measured in the short class; the full suite runs it")
	}
	for _, policy := range []string{"queue", "skip", "fail", "cancel_others"} {
		t.Run(policy, func(t *testing.T) {
			now := time.Now()
			server, tenant, owner := controllerCapacityFixture(t, teststore.WithClock(func() time.Time { return now }))
			plan := nodeproto.PlanDoc{
				Concurrency: []nodeproto.ConcurrencySpec{{Group: "serial", Scope: new("global"), Capacity: new(1), OnLimit: &policy, QueueTimeoutMS: new(int64(1))}},
				Jobs: []nodeproto.JobSpec{
					{ID: "first", Steps: []nodeproto.StepSpec{{ID: "body"}}},
					{ID: "next", Needs: []string{"first"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
				},
			}
			acceptCapacityDocument(t, server, tenant, owner, plan)
			claim, err := server.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "first"}})
			if err != nil {
				t.Fatal(err)
			}
			principal, err := server.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
			if err != nil {
				t.Fatal(err)
			}
			actor := engineActor(capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "first"}})
			ref := runspec.NodeRef{RunID: "run", NodeID: "first"}
			if result, err := tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: "run", NodeID: "first", Generation: int64(claim.Award.Generation)}, actor); err != nil || result.Kind != runstate.AcquireGranted {
				t.Fatalf("admission: %+v %v", result, err)
			}
			started, err := tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(0))})
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, server.store, `UPDATE concurrency_holders SET lease_expires_at=1 WHERE team='a' AND run_id='run' AND node_id=''`)
			other := addControllerCapacityRun(t, server, tenant)
			acceptCapacityDocumentForRun(t, server, tenant, other, "other", nodeproto.PlanDoc{Concurrency: plan.Concurrency, Jobs: []nodeproto.JobSpec{{ID: "hold", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
			for range 2 {
				server.reconcileOwnedRuns(t.Context())
				now = now.Add(time.Second)
			}
			state, err := tenant.GetConcurrencyState(t.Context(), "plan:global:serial")
			if err != nil || len(state.Waiters) != 1 || state.Waiters[0].RunID != "run" || state.Waiters[0].Policy != "queue" || state.Waiters[0].BlockingReason != "waiting to reacquire serial" || len(state.Holders) != 1 || state.Holders[0].RunID != "other" || state.Holders[0].Superseded {
				t.Fatalf("lapsed run did not queue normally: %+v %v", state, err)
			}
			response := httptest.NewRecorder()
			if err := server.handleQueueStateView(response, httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil), capability.Authorized{Target: capability.Target{Team: "a"}}); err != nil {
				t.Fatal(err)
			}
			var queue wingwire.QueueState
			if err := json.Unmarshal(response.Body.Bytes(), &queue); err != nil || len(queue.Waiters) != 1 || queue.Waiters[0].BlockingReason != "waiting to reacquire serial" {
				t.Fatalf("queue lost reacquisition state: %+v %v", queue, err)
			}
			row, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || row.Status != runstate.StatusRunning || row.CancelRequestedAt != nil {
				t.Fatalf("lapse stopped released work: %+v %v", row, err)
			}
			if _, err := tenant.TransitionNode(t.Context(), store.ReportNode{Ref: ref, Actor: actor, Outcome: runstate.NodeSuccess, SourceAttempt: &started.Attempt, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}}); err != nil {
				t.Fatalf("released work could not finish: %v", err)
			}
			next := runspec.NodeRef{RunID: "run", NodeID: "next"}
			row, err = tenant.ReadNode(t.Context(), next)
			if err != nil || row.ReadyAt != nil || row.Status != runstate.StatusPending {
				t.Fatalf("next job released before reacquisition: %+v %v", row, err)
			}
			if err := teststore.SeedRunFinish(server.store, t.Context(), "a", "other", "success", ""); err != nil {
				t.Fatal(err)
			}
			server.reconcileOwnedRuns(t.Context())
			row, err = tenant.ReadNode(t.Context(), next)
			if err != nil || row.ReadyAt == nil {
				t.Fatalf("next job did not release after reacquisition: %+v %v", row, err)
			}
		})
	}
}

func addControllerCapacityRun(t *testing.T, server *serverFixture, tenant *store.Tenant) capability.Principal {
	t.Helper()
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedRun(server.store, t.Context(), "a", store.Run{RunSummary: runstate.RunSummary{ID: "other", Pipeline: "ci", Status: "running"}}); err != nil {
		t.Fatal(err)
	}
	run.Request.RunID = "other"
	raw, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.store, `UPDATE runs SET request_json=?,dispatch='controller' WHERE team='a' AND id='other'`, raw)
	engineSQL(t, server.store, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) VALUES ('a','other','plan',1,1,?,?,?)`, time.Now().Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	claim, err := server.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitPlan, RunID: "other"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := server.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	return principal
}
