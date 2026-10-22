package controller

import (
	"crypto/sha256"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func engineAdmitExecution(t *testing.T, st *store.Store, principal capability.Principal) {
	t.Helper()
	c := principal.Capability
	tenant, err := st.ForTeam(t.Context(), store.Team(c.Team))
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), c.Binding.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Spec == nil {
		engineAcceptedFixturePlan(t, &engineFixture{Store: st}, store.Team(c.Team), c.Binding.RunID, c.Binding.NodeID)
	}
	a := capability.Authorized{Principal: principal, Binding: c.Binding, Target: capability.Target{Team: c.Team, RunID: c.Binding.RunID, NodeID: c.Binding.NodeID}}
	binding := store.CapacityBinding{RunID: c.Binding.RunID, NodeID: c.Binding.NodeID, Generation: c.Binding.Generation}
	if result, err := tenant.MayStart(t.Context(), binding, engineActor(a)); err != nil || result.Kind != store.AcquireGranted {
		t.Fatalf("execution admission: %+v, %v", result, err)
	}
}

func engineAdmitBearer(t *testing.T, st *store.Store, bearer string) {
	t.Helper()
	principal, err := st.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(bearer)))
	if err != nil {
		t.Fatal(err)
	}
	engineAdmitExecution(t, st, principal)
}
