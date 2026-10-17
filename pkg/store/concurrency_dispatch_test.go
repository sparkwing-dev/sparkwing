package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestConcurrencyControllerDispatchedRunUsesBoundUnitKind(t *testing.T) {
	tenant, plan, now := planFixture(t, runspec.DispatchController)
	plan.Seal.Plan = []byte(`{"jobs":[{"id":"job","concurrency":[{"group":"deploy"}]}]}`)
	job := plan.Seal.Resolved.Settings.Jobs["job"]
	job.Concurrency = []runspec.Concurrency{{Group: "deploy", Scope: "global", Capacity: 1, Cost: 1, OnLimit: "queue"}}
	plan.Seal.Resolved.Settings.Jobs["job"] = job
	tenant.s.intake = fakeIntakeContracts{resolve: func(settings.PlanInputs) (runspec.ExecutionSettings, error) { return plan.Seal.Resolved.Settings, nil }}
	if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	claim := authorityRoot("node-holder", "a")
	claim.Issuance, claim.Unit, claim.Purpose = capability.Award, capability.UnitNode, capability.PurposeWork
	claim.Parent = &plan.Seal.Request.AuthorityRef
	claim.Grants = []capability.Grant{capability.NodeReport}
	claim.Binding = capability.Binding{Kind: capability.BindingNode, RunID: plan.RunID, NodeID: "job", Holder: "node", Generation: 1}
	claim.UnitID, _ = encodeAwardUnit(UnitRef{Kind: capability.UnitNode, RunID: plan.RunID, NodeID: "job"})
	claim.Delivery = capability.DeliveryDirect
	claim.Billing = &capability.BillingProvenance{PayerTeam: "a", ReservationID: "award:node-holder", ExecutorClass: "runner", Start: capability.BillingAtStart, Unit: capability.BillingJob}
	authorityPersist(t, tenant.s, claim)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET claimed_by='node',capability_prefix='node-holder',generation=1,lease_expires_at=? WHERE team='a' AND run_id=? AND node_id='job'`, now.Add(DefaultConcurrencyLease).UnixNano(), plan.RunID)
	p, err := tenant.s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Prefix})
	if err != nil {
		t.Fatal(err)
	}
	route, _ := capability.LookupRoute("POST", "/api/v1/queue/groups/{group}/acquire")
	a := capability.Authorized{Principal: p, Binding: p.Capability.Binding, Access: route.Access[0], Target: capability.Target{Team: "a", RunID: plan.RunID, SlotKey: "global:deploy", SlotRunID: plan.RunID, SlotNodeID: "job", SlotGeneration: 1, SlotHolderID: "plan-slot"}}
	got, err := tenant.AcquireAuthorizedSlot(t.Context(), a, AcquireSlotRequest{Key: "global:deploy", HolderID: "plan-slot", RunID: plan.RunID, NodeID: "job"})
	if err != nil || got.Kind != AcquireGranted {
		t.Fatalf("controller-dispatched node slot: %+v %v", got, err)
	}
}
