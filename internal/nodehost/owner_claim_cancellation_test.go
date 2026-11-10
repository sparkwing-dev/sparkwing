package nodehost

import (
	"context"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestOwnerQueuedCancellationBeforeHeartbeatReportsCancelled(t *testing.T) {
	f := newStoreHostFixture(t, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job", Steps: []nodewire.StepSpec{{ID: "body"}}}}})
	input := storedExecutionInput(t, f, &ClaimedNodeHost{})
	host := f.Host
	host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error {
		t.Error("queued job started execution")
		return nil
	})
	host.executionAdmission = ComposeStartGate(&capacityProbe{admit: func(ctx context.Context) (runstate.AcquireSlotResponse, error) {
		if _, err := f.Tenant.TransitionRun(ctx, runstate.RunTransition{RunID: runspec.RunID(input.Accepted.RunID), Actor: f.Control, To: runstate.RunCancelled, Cause: runstate.RunCancel}); err != nil {
			t.Fatal(err)
		}
		response, err := f.Tenant.MayStart(ctx, runstate.CapacityBinding{RunID: input.Accepted.RunID, NodeID: input.NodeID, Generation: int64(input.Generation)}, executorActor(input))
		if err == nil {
			t.Fatal("cancelled claim retained start rights")
		}
		return response, err
	}}, nil, false)
	ref := runspec.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: runspec.NodeID(input.NodeID)}
	award, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.executeAward(t.Context(), RunInput{GrantedRunner: input.GrantedRunner}, input.Accepted, &source.Entry{Engine: input.Engine}, runstate.ReconciledNode{Node: award}); err != nil {
		t.Fatal(err)
	}
	row, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil || row.Outcome != runstate.NodeCancelled || row.FailureReason != string(hosterr.ReasonCancelled) || row.Attempt != 0 {
		t.Fatalf("queued self claim became a failure: %+v, %v", row, err)
	}
}

func TestOwnerReportsCancellationOnTheFirstSelfClaimRenewal(t *testing.T) {
	input, node, deps, _ := executorFixture(t)
	expires := time.Now().Add(time.Hour)
	node.row.LeaseExpiresAt = &expires
	state, host, _ := newOwnerFixture(t, true)
	state.snapshot.Nodes = []runstate.NodeRow{node.row}
	state.nodeRenew = func(context.Context, runspec.NodeRef, runstate.Actor) (runstate.LeaseBeat, error) {
		return runstate.LeaseBeat{CancelRequested: true, ExpiresAt: expires}, nil
	}
	host.Lease = lease.Keeper{Interval: time.Second, Lease: time.Minute}
	host.Masker = deps.Masker
	err := host.executeAward(t.Context(), RunInput{GrantedRunner: input.GrantedRunner}, input.Accepted, input.Entry, runstate.ReconciledNode{Node: node.row})
	if state.snapshot.Nodes[0].Outcome != runstate.NodeCancelled {
		t.Fatalf("initial cancellation left the claim unsettled: %+v, %v", state.snapshot.Nodes[0], err)
	}
	if len(state.trace) != 1 || state.trace[0] != string(runstate.NodeCancelled) {
		t.Fatalf("initial cancellation started work: %v", state.trace)
	}
}
