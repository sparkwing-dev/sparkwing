package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestControllerSettlementRetainsFailedDependencyReason(t *testing.T) {
	tenant, plan, _ := controllerPlanFixture(t, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job"}, {ID: "verify", Needs: []string{"job"}}}})
	actor := controllerJobActor(t, tenant, plan.RunID)
	if _, err := tenant.TransitionNode(t.Context(), ReportNode{Ref: runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "job"}, Actor: actor, SourceAttempt: sourceAttempt(1), Outcome: runstate.NodeFailed}); err != nil {
		t.Fatal(err)
	}
	job, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "verify"})
	if err != nil {
		t.Fatal(err)
	}
	if job.Outcome != runstate.NodeCancelled || job.Attempt != 0 || job.StartedAt != nil || job.FailureReason != "upstream_failed" {
		t.Fatalf("unstarted dependent lost its failure reason: %+v", job)
	}
}
