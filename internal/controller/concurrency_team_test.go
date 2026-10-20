package controller_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestTeamBoundary_ResolveWaiterDoesNotReadAnotherTeamsLeader(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		admitted, claim := tenantRunClaim(t, f)
		plan := nodeproto.PlanDoc{Concurrency: []nodeproto.ConcurrencySpec{{Group: "deploy"}}, Jobs: []nodeproto.JobSpec{}}
		declarations := runspec.CheckoutDeclarations{Version: 1}
		resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		auth := "Bearer " + claim.Capability
		submission := controllerwire.PlanSubmission{Declarations: declarations, Plan: raw, Resolved: runspec.Resolved{SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}
		if code, body := f.do("POST", "/api/v1/runs/"+admitted.RunID+"/plan", auth, submission); code != 200 {
			t.Fatalf("plan: %d %s", code, body)
		}
		acquire := map[string]any{"holder_id": "own-holder", "run_id": admitted.RunID, "job_id": ""}
		if code, body := f.do("POST", "/api/v1/queue/groups/global:deploy/acquire", auth, acquire); code != 200 {
			t.Fatalf("own slot: %d %s", code, body)
		}
		resolve := func(leader, forged string) string {
			t.Helper()
			query := url.Values{"run_id": {admitted.RunID}, "job_id": {""}, "leader_run_id": {leader}, "leader_job_id": {"n1"}, "team": {forged}}
			code, body := f.do("GET", "/api/v1/queue/groups/global:deploy/resolve?"+query.Encode(), auth, nil)
			if code != http.StatusForbidden {
				t.Fatalf("foreign/missing leader: %d %s", code, body)
			}
			return body
		}
		missing := resolve("missing", "")
		if err := quick.Check(func(forged string) bool {
			body := resolve(f.runA, forged)
			return body == missing && !strings.Contains(body, "leader_outcome") && !strings.Contains(body, f.victim)
		}, &quick.Config{MaxCount: 32}); err != nil {
			t.Fatal(err)
		}
	})
}
