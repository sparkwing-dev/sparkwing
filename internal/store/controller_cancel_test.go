package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestControllerCancelAfterUnstartedAwardExpires(t *testing.T) {
	tenant, request, now := controllerPlanFixture(t, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job"}}})
	controllerJobAward(t, tenant, request.RunID)
	ref := runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"}
	job, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || job.LeaseExpiresAt == nil || job.Attempt != 0 {
		t.Fatalf("unstarted award: %+v %v", job, err)
	}
	*now = *job.LeaseExpiresAt
	if reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), 10); err != nil || len(reaped) != 1 || !reaped[0].Requeued {
		t.Fatalf("expired award: %+v %v", reaped, err)
	}
	if _, err := tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: ref.RunID, To: runstate.RunCancelled, Cause: runstate.RunCancel, Actor: controllerActor(request.RunID), Reason: "cancel_requested"}); err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.State != runstate.RunCancelled || run.FinishedAt == nil {
		t.Fatalf("cancel after expiry: %s %v", run.State, err)
	}
	job, err = tenant.ReadNode(t.Context(), ref)
	if err != nil || job.Outcome != runstate.NodeCancelled || job.Attempt != 0 {
		t.Fatalf("cancelled unstarted job: %+v %v", job, err)
	}
}
