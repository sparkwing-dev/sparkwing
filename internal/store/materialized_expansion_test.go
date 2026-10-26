package store

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestMaterializedGeneratorDelegatesAndSettlesAtGenerationZero(t *testing.T) {
	tenant, request := materializationFixture(t, runstate.SourceManualRetry)
	owner := authorityRoot("owner", "a")
	owner.Subject.ID = "host"
	owner.Grants = []capability.Grant{capability.TriggerHost, capability.NodesClaim, capability.ExpansionSubmit}
	owner.Unit, owner.UnitID, owner.Purpose = capability.UnitTrigger, "run", capability.PurposeTrigger
	owner.Delivery = capability.DeliveryDirect
	root := authorityRoot("expansion-root", "a")
	root.Grants = owner.Grants
	authorityPersist(t, tenant.s, root)
	owner.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	owner.Billing = &capability.BillingProvenance{PayerTeam: "a", ReservationID: "owner", ExecutorClass: "runner", Start: capability.BillingAtAward, Unit: capability.BillingHost}
	owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "host", Generation: 1}
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM capabilities WHERE prefix='owner'`)
	authorityPersist(t, tenant.s, owner)
	plan := []byte(`{"jobs":[{"id":"job","closures":{"generate":["group"]}}]}`)
	spec, err := json.Marshal(runspec.Spec{Version: 1, Declarations: runspec.CheckoutDeclarations{Version: 1}, Request: runspec.SealedRequest{Version: 1, RunID: "run", Request: runspec.Request{Pipeline: "p", Dispatch: runspec.DispatchHost}, Provenance: assertedProvenance(t, "p", "", "")}})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET spec_json=?,spec_hash='spec',plan_hash='plan',plan_json=?,sealed_at=1 WHERE id='run'`, spec, plan)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET spec_json=?,expansion='expected' WHERE run_id='run'`, []byte(`{"id":"job","closures":{"generate":["group"]}}`))
	materialized, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil || materialized.Kind != runstate.Materialize || materialized.Node.Generation != 0 || materialized.Node.Outcome != runstate.NodeSuccess || materialized.Node.Expansion != runstate.ExpansionExpected {
		t.Fatalf("materialized source=%+v error=%v", materialized, err)
	}
	issued, err := tenant.s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{
		Parent: request.Actor.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.ExpansionSubmit},
		Binding: capability.Binding{
			Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: int64(materialized.Node.Generation),
			Expansion: &capability.LeaseRef{Kind: capability.UnitTrigger, ID: "run", Holder: "host", Generation: 1},
		},
	})
	if err != nil {
		t.Fatalf("delegate materialized source: %v", err)
	}
	route, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/expansion")
	if !ok {
		t.Fatal("expansion route is missing")
	}
	if _, err := capability.NewAuthenticator(tenant.s.AsOperator()).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: issued.Raw}, route); err != nil {
		t.Fatalf("authenticate materialized expansion: %v", err)
	}
	actor := request.Actor
	actor.Authority.ID, actor.CapabilityPrefix = issued.Capability.Prefix, issued.Capability.Prefix
	accepted, err := tenant.AcceptExpansion(t.Context(), ExpansionSubmission{Source: request.Ref, SourceGeneration: 0, Actor: actor, Registration: runspec.SecretRegistration{Version: 1}, Body: []byte(`{"jobs":[]}`)})
	if err != nil || accepted.State != runstate.ExpansionAccepted || accepted.SourceGeneration != 0 {
		t.Fatalf("accept materialized expansion=%+v error=%v", accepted, err)
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := snapshot.Scheduling()
	if err != nil || Decide(projection).Outcome != "success" {
		t.Fatalf("materialized generator left settlement barrier unresolved: %+v error=%v", projection, err)
	}
}
