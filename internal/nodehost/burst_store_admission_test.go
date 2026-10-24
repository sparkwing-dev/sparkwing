package nodehost

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestBurstBusyComputeRelinquishesPermitCapacityAndAward(t *testing.T) {
	request := runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}, Burst: &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here"}, {Target: "cloud"}}}}
	request.Options.OwnerExecutionTimeout = runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Hour}}
	fixture := newStoreHostFixtureForRequest(t, openStore(t), capacityPlan("queue", 0, "job"), request)
	ref := runspec.NodeRef{RunID: runspec.RunID(fixture.Input.RunID), NodeID: "job"}
	if _, err := fixture.Tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: fixture.Actor}); err != nil {
		t.Fatal(err)
	}
	before := capacityRow(t, fixture, "job")
	award, err := fixture.Tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: fixture.Actor, Policy: runstate.SourceNone})
	if err != nil || award.Kind != runstate.Execute {
		t.Fatalf("provisional award=%+v error=%v", award, err)
	}
	entry, err := fixture.Host.Acquire(t.Context(), fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewNodeInput(fixture.Accepted, award.Node, entry.Engine, fixture.Input.GrantedRunner)
	if err != nil {
		t.Fatal(err)
	}
	compute := &LocalAdmission{Daemon: admissionAcquireFunc(func(ctx context.Context, req wingwire.AdmissionRequest, _ func(wingwire.Queued)) (Lease, error) {
		state, err := fixture.Tenant.GetConcurrencyState(ctx, "job:global:db")
		if err != nil || state.UsedCost != 1 || len(state.Holders) != 1 || !req.NonBlocking {
			t.Fatalf("compute precedes durable logical grant: state=%+v request=%+v error=%v", state, req, err)
		}
		return nil, &wingdclient.AdmissionError{Key: "capacity", Policy: wingwire.PolicyFail}
	})}
	gate := ComposeStartGate(fixture.Tenant, compute, true)
	if err := gate.claimedNode(t.Context(), input, func(context.Context) error { t.Fatal("busy compute executed"); return nil }); err != nil {
		t.Fatal(err)
	}
	after := capacityRow(t, fixture, "job")
	if after.ClaimedBy != "" || after.CapabilityPrefix != "" || after.Outcome != "" || after.Attempt != 0 || after.ReadyAt == nil || !after.ReadyAt.Equal(*before.ReadyAt) {
		t.Fatalf("relinquishment changed readiness or retained award: %+v", after)
	}
	state, err := fixture.Tenant.GetConcurrencyState(t.Context(), "job:global:db")
	if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 0 {
		t.Fatalf("relinquishment retained logical capacity: %+v error=%v", state, err)
	}
	if _, err := fixture.Tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: executorActor(input), SourceAttempt: &input.Attempt}); err == nil {
		t.Fatal("relinquished award retained its execution permit")
	}
}
