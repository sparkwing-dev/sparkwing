package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestUnstartedReturnedAwardKeepsTheJobsQueueStart(t *testing.T) {
	tenant, request, now := planFixture(t, runspec.DispatchController)
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	root := authorityRoot("executor", "a")
	root.Grants = []capability.Grant{capability.NodesClaim}
	authorityPersist(t, tenant.s, root)
	ref := runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"}
	before, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || before.ReadyAt == nil || before.QueueStartedAt == nil {
		t.Fatalf("job not ready: %+v %v", before, err)
	}
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}})
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(4 * time.Second)
	if err := tenant.s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	after, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || after.QueueStartedAt == nil || !after.QueueStartedAt.Equal(*before.QueueStartedAt) {
		t.Fatalf("an unstarted return moved the queue start from %v to %v (%v), renewing placement holds", before.QueueStartedAt, after.QueueStartedAt, err)
	}
}
