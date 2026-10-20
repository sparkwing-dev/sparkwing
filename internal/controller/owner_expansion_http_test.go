package controller

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestOwnerExpansionHTTPDerivesSourceAuthority(t *testing.T) {
	for _, report := range []struct {
		name, body string
		state      store.ExpansionState
	}{
		{"success", `{"source_generation":3,"body":{"jobs":[]}}`, store.ExpansionAccepted},
		{"failure", `{"source_generation":3,"failure":{"code":"sdk_error","message":"generator failed"}}`, store.ExpansionFailed},
	} {
		t.Run(report.name, func(t *testing.T) {
			engine, tenant := engineRuntimeStore(t)
			owner := engineRuntimeClaim(t, engine, capability.UnitTrigger)
			raw := nativeClaimPresentation(t, engine, owner)
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			spec, err := json.Marshal(runspec.Spec{Version: 1, Declarations: runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}, Request: run.Request, Resolved: runspec.Resolved{Guards: runspec.Guards{Passed: true}}})
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, engine.Store, `UPDATE runs SET plan_json=?,plan_hash='plan',sealed_at=1,spec_json=?,spec_hash='spec'`, []byte(`{"jobs":[{"id":"job","closures":{"generate":["group"]}}]}`), spec)
			engineSQL(t, engine.Store, `INSERT INTO nodes(team,run_id,node_id,status,outcome,generation,attempt,finished_at,expansion,spec_json,execution_json) VALUES ('a','run','job','done','success',3,2,1,'expected',?,?)`, []byte(`{"id":"job","closures":{"generate":["group"]}}`), []byte(`{}`))
			server := New(engine.Store, nil)
			for range 2 {
				engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/expansion", report.body, 200)
			}
			node, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || node.Expansion != report.state || node.Attempt != 2 || node.Generation != 3 {
				t.Fatalf("expansion source changed: %+v %v", node, err)
			}
			engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/expansion", `{"source_generation":4,"failure":{"code":"sdk_error","message":"wrong source"}}`, 409)
		})
	}
}
