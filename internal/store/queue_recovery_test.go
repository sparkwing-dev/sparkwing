package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestUnstartedNodeRequeuesKeepQueueDeadline(t *testing.T) {
	tenant, request, now := planFixtureWithInput(t, runspec.DispatchHost, func(input *IntakeRequest) {
		input.Request.Options.ClaimWait.Optional = runspec.Optional[time.Duration]{Set: true, Value: 20 * time.Minute}
	})
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	root := authorityRoot("executor", "a")
	root.Grants = []capability.Grant{capability.NodesClaim}
	authorityPersist(t, tenant.s, root)
	lease, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ref := controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"}
	initial := *now
	for i := range 4 {
		if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, Actor: request.Actor, To: NodeReady, Cause: CauseRelease}); err != nil {
			t.Fatal(err)
		}
		claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}})
		if err != nil {
			t.Fatal(err)
		}
		*now = claim.Award.LeaseExpiresAt
		reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10)
		if err != nil || len(reaped) != 1 || !reaped[0].Requeued || reaped[0].Disruptions != 0 || reaped[0].Generation != int64(i+1) {
			t.Fatalf("unstarted loss %d: %+v, %v", i+1, reaped, err)
		}
	}
	*now = initial.Add(20 * time.Minute)
	reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10)
	if err != nil || len(reaped) != 1 || !reaped[0].Failed || reaped[0].Disruptions != 0 {
		t.Fatalf("original queue deadline: %+v, %v", reaped, err)
	}
	node, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || node.Outcome != NodeFailed || node.Attempt != 0 {
		t.Fatalf("expired unstarted job: %+v, %v", node, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "node_attempts", 0)
}

func TestStartedNodeDisruptionGetsNewQueueInterval(t *testing.T) {
	tenant, request, now := planFixtureWithInput(t, runspec.DispatchHost, func(input *IntakeRequest) {
		input.Request.Options.ClaimWait.Optional = runspec.Optional[time.Duration]{Set: true, Value: 20 * time.Minute}
	})
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	root := authorityRoot("executor", "a")
	root.Grants = []capability.Grant{capability.NodesClaim}
	authorityPersist(t, tenant.s, root)
	lease, err := tenant.s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "replica", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ref := controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "job"}
	initial := *now
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, Actor: request.Actor, To: NodeReady, Cause: CauseRelease}); err != nil {
		t.Fatal(err)
	}
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}})
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
	admitExecution(t, tenant, actor)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	*now = claim.Award.LeaseExpiresAt
	if _, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil {
		t.Fatal(err)
	}
	requeued := *now
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, Actor: request.Actor, To: NodeReady, Cause: CauseRelease}); err != nil {
		t.Fatal(err)
	}
	*now = initial.Add(20 * time.Minute)
	if reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 0 {
		t.Fatalf("previous queue charged execution time: %+v, %v", reaped, err)
	}
	*now = requeued.Add(20 * time.Minute)
	if reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10); err != nil || len(reaped) != 1 || !reaped[0].Failed {
		t.Fatalf("new queue deadline: %+v, %v", reaped, err)
	}
}
