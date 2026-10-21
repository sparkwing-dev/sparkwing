package controller_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestTeamBoundary_ANodeCannotAdoptAnotherTeamsAttemptHistory(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `UPDATE nodes SET status='done',outcome='failed',attempt=1,generation=1,finished_at=$3 WHERE team=$1 AND run_id=$2 AND node_id='n1'`, f.teamA.Team(), f.runA, time.Now().UnixNano()); err != nil {
			t.Fatal(err)
		}
		if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome,failure_reason) VALUES ($1,$2,'n1',1,1,'holder-a','claim-a','runner','team-a-box','self-hosted',$3,$3,'failed','team A private failure')`, f.teamA.Team(), f.runA, time.Now().UnixNano()); err != nil {
			t.Fatal(err)
		}
		if code, body := f.do("GET", "/api/v1/runs/"+f.runA+"/jobs/n1/attempts", f.ownerA, nil); code != 200 || !strings.Contains(body, "team A private failure") || !strings.Contains(body, `"attempt":1`) {
			t.Fatalf("owner attempt = %d: %s", code, body)
		}
		admitted, claim := tenantRunClaim(t, f)
		plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "n1", Steps: []nodeproto.StepSpec{{ID: "work"}}}}}
		declarations := runspec.CheckoutDeclarations{Version: 1}
		resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan})
		if err != nil {
			t.Fatal(err)
		}
		rawPlan, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		submission := controllerwire.PlanSubmission{Declarations: declarations, Plan: rawPlan, Resolved: runspec.Resolved{SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}
		path := "/api/v1/runs/" + admitted.RunID
		property := func(value string) bool {
			for _, root := range []string{f.runA, "forged-" + value} {
				var document struct {
					Jobs []map[string]json.RawMessage `json:"jobs"`
				}
				if err := json.Unmarshal(rawPlan, &document); err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				document.Jobs[0]["retry_root_run_id"] = encoded
				altered := submission
				altered.Plan, err = json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				code, body := f.do("POST", path+"/plan", "Bearer "+claim.Capability, altered)
				if code != http.StatusUnprocessableEntity || strings.Contains(body, "team A private failure") || strings.Contains(body, "team-a-box") {
					t.Fatalf("forged attempt lineage = %d: %s", code, body)
				}
			}
			snapshot, err := f.teamB.ReadSnapshot(t.Context(), admitted.RunID)
			return err == nil && snapshot.Run.Spec == nil && len(snapshot.Nodes) == 0
		}
		if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
			t.Fatal(err)
		}
		if code, body := f.do("POST", path+"/plan", "Bearer "+claim.Capability, submission); code != 200 {
			t.Fatalf("own plan = %d: %s", code, body)
		}
		node, err := f.teamB.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(admitted.RunID), NodeID: "n1"})
		if err != nil || node.Attempt != 0 || node.Generation != 0 {
			t.Fatalf("own initial job = %+v: %v", node, err)
		}
		for _, suffix := range []string{"/jobs", "/jobs/n1", "/jobs/n1/attempts"} {
			code, body := f.do("GET", path+suffix, f.everyScopeB, nil)
			if code != 200 || strings.Contains(body, "team A private failure") || strings.Contains(body, "team-a-box") || strings.Contains(body, f.runA) {
				t.Fatalf("own job view %s = %d: %s", suffix, code, body)
			}
		}
		view, err := f.teamB.ReadRunView(t.Context(), admitted.RunID)
		if err != nil || len(view.Attempts) != 0 {
			t.Fatalf("foreign attempts persisted: %+v: %v", view.Attempts, err)
		}
	})
}
