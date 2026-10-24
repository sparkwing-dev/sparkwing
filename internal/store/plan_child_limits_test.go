package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestPlanChildLimitUsesRemainingRootAllowance(t *testing.T) {
	for _, dispatch := range []runspec.Dispatch{runspec.DispatchHost, runspec.DispatchController} {
		for _, tc := range []struct {
			name                    string
			cap, jobs, children     int64
			existing, nested, retry bool
			refused                 bool
		}{
			{name: "child declarations", cap: 1, jobs: 1, children: 2, refused: true},
			{name: "declared fan-out", cap: 1, jobs: 2, children: 1, refused: true},
			{name: "remaining", cap: 2, jobs: 2, children: 1, existing: true, refused: true},
			{name: "nested root", cap: 2, jobs: 2, children: 1, existing: true, nested: true, refused: true},
			{name: "exact remaining", cap: 3, jobs: 2, children: 1, existing: true},
			{name: "no declared children", cap: 1, existing: true},
			{name: "retries excluded", cap: 2, jobs: 2, children: 1, existing: true, retry: true},
		} {
			t.Run(string(dispatch)+"/"+tc.name, func(t *testing.T) {
				tenant, request, _ := planFixture(t, dispatch)
				if tc.existing {
					other, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: request.Seal.Request.AuthorityRef, Request: request.Seal.Request.Request})
					if err != nil {
						t.Fatal(err)
					}
					child, root := other.RunID, request.RunID
					if tc.nested {
						child, root = root, child
					}
					retry := ""
					if tc.retry {
						retry = "previous"
					}
					rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET parent_run_id=?,retry_of=?,lineage_json=? WHERE team='a' AND id=?`, root, retry, []byte(fmt.Sprintf(`{"parent_run_id":%q,"root_run_id":%q}`, root, root)), child)
				}
				rewriteSchemaExec(t, tenant.s.db, `UPDATE teams SET max_children_per_root=? WHERE name='a'`, tc.cap)
				plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}
				for i := range tc.jobs {
					job := nodeproto.JobSpec{ID: fmt.Sprintf("fan-%d", i), Steps: []nodeproto.StepSpec{{ID: "body"}}}
					for k := range tc.children {
						job.Children = append(job.Children, nodeproto.ChildPipeline{Pipeline: fmt.Sprintf("child-%d", k)})
					}
					plan.Jobs = append(plan.Jobs, job)
				}
				var err error
				request.Seal.Plan, err = json.Marshal(testBodyPlan(plan))
				if err != nil {
					t.Fatal(err)
				}
				request.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Request: request.Seal.Request, Declarations: request.Seal.Declarations, Plan: plan})
				if err != nil {
					t.Fatal(err)
				}
				_, err = tenant.AcceptPlan(t.Context(), request)
				if tc.refused {
					var refusal *apierr.Error
					observed := int64(0)
					if tc.existing {
						observed = 1
					}
					if !errors.As(err, &refusal) || refusal.Code != apierr.Conflict || refusal.Message != fmt.Sprintf("This team allows %d child runs per root run (maximum 1,000). Raise the team's child-run limit or split the work into separate runs.", tc.cap) || refusal.Details["limit"] != "max_children_per_root" || refusal.Details["cap"] != tc.cap || refusal.Details["observed"] != observed {
						t.Fatalf("plan child limit: %+v, %v", refusal, err)
					}
					run, readErr := tenant.ReadRun(t.Context(), request.RunID)
					if readErr != nil || run.Spec != nil {
						t.Fatalf("refused plan persisted: %+v, %v", run.Spec, readErr)
					}
					rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
				} else {
					if err != nil {
						t.Fatal(err)
					}
					rewriteSchemaExec(t, tenant.s.db, `UPDATE teams SET max_children_per_root=1 WHERE name='a'`)
					if replay, err := tenant.AcceptPlan(t.Context(), request); err != nil || !replay.Replayed {
						t.Fatalf("accepted replay after limit reduction: %+v, %v", replay, err)
					}
				}
			})
		}
	}
}
