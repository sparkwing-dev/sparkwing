package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestControllerCancelUnclaimableJobFinishesRun(t *testing.T) {
	s, options, _, _ := retryActionDispatchFixture(t, "value", true, runspec.DispatchController)
	control, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := control.Retry(t.Context(), "run", controllerwire.RetryRequest{Mode: controllerwire.RetryAll, IdempotencyKey: "unclaimable"})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE nodes SET execution_json=? WHERE team='a' AND run_id=?`, []byte(`{"requires":{"set":true,"value":["matrix-label=absent"]}}`), created.RunID)
	s.reconcileOwnedRuns(t.Context())
	tenant := teststore.ForTeam(t, s.store, "a")
	ref := runspec.NodeRef{RunID: runspec.RunID(created.RunID), NodeID: "job"}
	job, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || job.ReadyAt == nil || job.Attempt != 0 {
		t.Fatalf("unclaimed job: %+v %v", job, err)
	}
	engineHTTPRoot(t, s.engine, "a", "worker", []capability.Grant{capability.NodesClaim})
	if claim, err := s.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "worker", Kinds: []capability.UnitKind{capability.UnitNode}}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unavailable label awarded: %+v %v", claim, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := control.Cancel(ctx, created.RunID, controllerwire.CancelRequest{Reason: "cancel_requested"}); err != nil {
		t.Fatal(err)
	}
	run, err := control.ReadRun(ctx, created.RunID)
	if err != nil || run.Status != "cancelled" || run.FinishedAt == nil {
		t.Fatalf("cancelled run: %+v %v", run, err)
	}
	job, err = tenant.ReadNode(ctx, ref)
	if err != nil || job.Outcome != runstate.NodeCancelled || job.Attempt != 0 {
		t.Fatalf("cancelled job: %+v %v", job, err)
	}
}

func TestControllerCancelCapacityWaiterKeepsHolder(t *testing.T) {
	server, tenant, owner := controllerCapacityFixture(t)
	const key = "plan:global:serial"
	seedOtherCapacityHolder(t, server, key)
	acceptCapacityDocument(t, server, tenant, owner, nodewire.PlanDoc{
		Concurrency: []nodewire.ConcurrencySpec{{Group: "serial", Scope: new("global"), Capacity: new(1), OnLimit: new("queue")}},
		Jobs:        []nodewire.JobSpec{{ID: "job", Steps: []nodewire.StepSpec{{ID: "body"}}}},
	})
	before, err := tenant.GetConcurrencyState(t.Context(), key)
	if err != nil || len(before.Waiters) != 1 || before.Waiters[0].RunID != "run" || len(before.Holders) != 1 || before.Holders[0].RunID != "other" {
		t.Fatalf("capacity wait: %+v %v", before, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	actor := runstate.Actor{Kind: runstate.ActorController, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController, EvalFence: runspec.EvalFence{RunID: "run"}}}
	if _, err := tenant.TransitionRun(ctx, runstate.RunTransition{RunID: "run", To: runstate.RunCancelled, Cause: runstate.RunCancel, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(ctx, "run")
	if err != nil || run.State != runstate.RunCancelled || run.FinishedAt == nil {
		t.Fatalf("cancelled waiter: %s %v", run.State, err)
	}
	job, err := tenant.ReadNode(ctx, runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || job.Outcome != runstate.NodeCancelled || job.Attempt != 0 {
		t.Fatalf("cancelled job: %+v %v", job, err)
	}
	after, err := tenant.GetConcurrencyState(ctx, key)
	if err != nil || len(after.Waiters) != 0 || len(after.Holders) != 1 || after.Holders[0].RunID != "other" || !after.Holders[0].ClaimedAt.Equal(before.Holders[0].ClaimedAt) {
		t.Fatalf("holder changed on waiter cancellation: %+v %v", after, err)
	}
	holder, err := tenant.ReadRun(ctx, "other")
	if err != nil || holder.State != runstate.RunRunning || holder.CancelRequestedAt != nil {
		t.Fatalf("holder was cancelled: %s %v", holder.State, err)
	}
}
