package controller

import (
	"crypto/sha256"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// LauncherFixtureClaim provisions a launcher and exchanges its current unit award.
func LauncherFixtureClaim(t *testing.T, st *store.Store, runID, nodeID string) string {
	t.Helper()
	st.SetProvisionPolicy(sessionOriginFake{})
	root, err := st.ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionBootstrap, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "fixture-bootstrap"},
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens},
	})
	st.SetProvisionPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := st.ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionService, Authority: &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix},
		RegistrationID: "service:" + root.Capability.Prefix, Subject: capability.Subject{Kind: capability.SubjectService, ID: root.Capability.Prefix},
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch},
	})
	if err != nil {
		t.Fatal(err)
	}
	unit := store.UnitRef{Kind: capability.UnitNode, RunID: runID, NodeID: nodeID}
	if nodeID == store.PlanNodeID {
		unit.Kind, unit.NodeID = capability.UnitPlan, ""
	}
	claim, err := st.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: launcher.Capability.Prefix, Target: &unit})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := st.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Raw
}

func TestLauncherFixtureUsesCurrentUnitAward(t *testing.T) {
	for _, nodeID := range []string{store.PlanNodeID, "job"} {
		t.Run(nodeID, func(t *testing.T) {
			engine, _ := engineRuntimeStore(t)
			st := engine.Store
			engineSQL(t, st, `UPDATE runs SET dispatch='controller'`)
			engineSQL(t, st, `UPDATE triggers SET unit_kind='plan'`)
			engineSQL(t, st, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
			raw := LauncherFixtureClaim(t, st, "run", nodeID)
			principal, err := st.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
			if err != nil {
				t.Fatal(err)
			}
			unit, job := capability.UnitNode, nodeID
			if nodeID == store.PlanNodeID {
				unit, job = capability.UnitPlan, ""
			}
			c := principal.Capability
			if c.Unit != unit || c.Delivery != capability.DeliveryDirect || c.Binding.RunID != "run" || c.Binding.NodeID != job || c.Binding.Generation != 1 {
				t.Fatalf("exchanged execution identity: unit=%s delivery=%s binding=%+v", c.Unit, c.Delivery, c.Binding)
			}
		})
	}
}
