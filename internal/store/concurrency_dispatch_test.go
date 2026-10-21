package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestConcurrencyControllerDispatchedRunUsesBoundUnitKind(t *testing.T) {
	tenant, plan, now := planFixture(t, runspec.DispatchController)
	plan.Seal.Plan = []byte(`{"jobs":[{"id":"job","steps":[{"id":"body"}],"concurrency":[{"group":"deploy"}]}]}`)
	job := plan.Seal.Resolved.Settings.Jobs["job"]
	job.Concurrency = []runspec.Concurrency{{Group: "deploy", Scope: "global", Capacity: 1, Cost: 1, OnLimit: "queue"}}
	plan.Seal.Resolved.Settings.Jobs["job"] = job
	if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	claim := authorityRoot("node-holder", "a")
	claim.Issuance, claim.Unit, claim.Purpose = capability.Award, capability.UnitNode, capability.PurposeWork
	claim.Parent = &plan.Seal.Request.AuthorityRef
	claim.Grants = []capability.Grant{capability.NodeReport}
	claim.Binding = capability.Binding{Kind: capability.BindingNode, RunID: plan.RunID, NodeID: "job", Holder: "node", Generation: 1}
	claim.UnitID, _ = encodeAwardUnit(UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(plan.RunID), NodeID: "job"})
	claim.Delivery = capability.DeliveryDirect
	claim.Billing = &capability.BillingProvenance{PayerTeam: "a", ReservationID: "award:node-holder", ExecutorClass: "runner", Start: capability.BillingAtStart, Unit: capability.BillingJob}
	authorityPersist(t, tenant.s, claim)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET claimed_by='node',capability_prefix='node-holder',generation=1,lease_expires_at=? WHERE team='a' AND run_id=? AND node_id='job'`, now.Add(defaultConcurrencyLease).UnixNano(), plan.RunID)
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Prefix}, CapabilityPrefix: claim.Prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(plan.RunID), NodeID: "job", Holder: "node", Generation: 1}}
	got, err := tenant.MayStart(t.Context(), CapacityBinding{RunID: plan.RunID, NodeID: "job", Generation: 1}, actor)
	if err != nil || got.Kind != AcquireGranted {
		t.Fatalf("controller-dispatched node admission: %+v %v", got, err)
	}
}
