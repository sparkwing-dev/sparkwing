package nodehost

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

type cancelBeforeCompletion struct {
	OwnerStore
	control runstate.Actor
}

func (s cancelBeforeCompletion) FinishRunAndUnit(ctx context.Context, request runstate.FinishRunRequest) (runstate.Applied, error) {
	if _, err := s.OwnerStore.TransitionRun(ctx, runstate.RunTransition{RunID: request.Transition.RunID, Actor: s.control, To: runstate.RunCancelled, Cause: runstate.RunCancel, Reason: "operator cancelled"}); err != nil {
		return runstate.Applied{}, err
	}
	return s.OwnerStore.FinishRunAndUnit(ctx, request)
}

func TestOwnerReportsCancellationCommittedAfterScheduling(t *testing.T) {
	fixture := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}})
	fixture.Host.Store = cancelBeforeCompletion{OwnerStore: fixture.State, control: fixture.Control}
	completion, err := fixture.Host.Run(t.Context(), fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	row, err := fixture.Tenant.ReadRun(t.Context(), fixture.Input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != runstate.RunCancelled || completion.Outcome != string(row.State) || completion.Reason != row.CancelReason {
		t.Fatalf("reported %+v, committed %s (%s)", completion, row.State, row.CancelReason)
	}
}

func TestOwnerFixtureReturnsCommittedVerdict(t *testing.T) {
	fixture, _, input := newOwnerFixture(t, true)
	applied, err := fixture.FinishRunAndUnit(t.Context(), runstate.FinishRunRequest{Unit: input.Owner.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(input.RunID), Actor: input.Owner, Cause: runstate.RunFinish}})
	if err != nil || applied.Outcome != runstate.RunSuccess || fixture.snapshot.Run.State != applied.Outcome {
		t.Fatalf("completion=%+v state=%s error=%v", applied, fixture.snapshot.Run.State, err)
	}
}

type completionController struct {
	controllerwire.Controller
	result controllerwire.Applied
}

func (c completionController) Finish(context.Context, string, controllerwire.FinishRequest) (controllerwire.Applied, error) {
	return c.result, nil
}

func TestRemoteCompletionRetainsCommittedVerdict(t *testing.T) {
	transport := &RemoteHostStore{controller: completionController{result: controllerwire.Applied{Outcome: "cancelled", Reason: "operator cancelled", StateSeq: 8}}}
	applied, err := transport.FinishRunAndUnit(t.Context(), runstate.FinishRunRequest{Transition: runstate.RunCompletion{RunID: "run", Cause: runstate.RunFinish, Reason: "scheduled success"}})
	if err != nil || applied.Outcome != runstate.RunCancelled || applied.Reason != "operator cancelled" || applied.StateSeq != 8 {
		t.Fatalf("completion=%+v error=%v", applied, err)
	}
}
