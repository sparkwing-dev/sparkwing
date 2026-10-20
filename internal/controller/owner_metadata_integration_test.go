//go:build rewrite_integration

package controller

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestReviewOwnerEvaluationReportsBeforeWorkAward(t *testing.T) {
	for _, report := range []struct{ suffix, body string }{
		{"annotations", `{"level":"info","message":"owner evaluation"}`},
		{"summary", `{"markdown":"owner evaluation"}`},
	} {
		t.Run(report.suffix, func(t *testing.T) {
			engine, _ := engineRuntimeStore(t)
			authorized := engineRuntimeClaim(t, engine, capability.UnitTrigger)
			raw := nativeClaimPresentation(t, engine, authorized)
			engineSQL(t, engine.Store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run','job','pending',?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
			server := New(engine.Store, nil)
			engineClaimHTTP(t, server.Handler(), raw, "POST", "/api/v1/runs/run/jobs/job/"+report.suffix, report.body, 204)
		})
	}
}
