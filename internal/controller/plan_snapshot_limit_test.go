package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func controllerPlanFixture(t *testing.T, plan nodeproto.PlanDoc, opts ...store.Option) (*engineFixture, *store.Tenant, http.Handler, string, []byte) {
	t.Helper()
	e, tenant := engineRuntimeStore(t, opts...)
	engineSQL(t, e.Store, `UPDATE runs SET dispatch='controller'`)
	engineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan'`)
	raw := LauncherFixtureClaim(t, e.Store, "run", store.PlanNodeID)
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Dispatch = runspec.DispatchController
	request, err := runspec.SealRequest(run.Request.Request, runspec.IntakeFacts{RunID: run.ID, Provenance: run.Request.Provenance})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET request_json=?,request_hash=? WHERE id='run'`, encoded, string(request.Hash))
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: request, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	rawPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(controllerwire.PlanSubmission{
		Plan: rawPlan, Declarations: declarations,
		Resolved: runspec.Resolved{SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := New(e.Store, nil).WithEngineController(e.EngineController).Handler()
	return e, tenant, h, raw, body
}

func TestPlanSubmissionBodyLimitPreservesCommittedPlan(t *testing.T) {
	_, tenant, h, raw, body := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}})
	refuse := func(body string, status int, code apierr.Code) {
		t.Helper()
		before, err := tenant.ReadSnapshot(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		w := engineClaimHTTP(t, h, raw, http.MethodPost, "/api/v1/runs/run/plan", body, status)
		var failure apierr.Envelope
		if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || failure.Error.Code != code {
			t.Fatalf("oversized plan: %s (%v)", w.Body, err)
		}
		after, err := tenant.ReadSnapshot(t.Context(), "run")
		before.StoreNow = after.StoreNow
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("oversized submission changed the run snapshot: %v", err)
		}
	}
	prefix := string(bytes.TrimSuffix(body, []byte(`}`)))
	oversized := prefix + strings.Repeat(" ", maxPlanSnapshotBody+1-len(body)) + "}"
	refuse(oversized, http.StatusBadRequest, apierr.BadRequest)
	atLimit := prefix + strings.Repeat(" ", maxPlanSnapshotBody-len(body)) + "}"
	engineClaimHTTP(t, h, raw, http.MethodPost, "/api/v1/runs/run/plan", atLimit, http.StatusOK)
	committed, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || committed.Spec == nil || len(committed.Plan) == 0 {
		t.Fatalf("plan did not commit: %v", err)
	}
	refuse(oversized, http.StatusUnauthorized, apierr.Unauthenticated)
}

func TestComputeLimits_PlanInsertionRefusedPastThePerRunCap(t *testing.T) {
	e, tenant, h, raw, body := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "one", Steps: []nodeproto.StepSpec{{ID: "work"}}}, {ID: "two", Steps: []nodeproto.StepSpec{{ID: "work"}}}}})
	if _, err := e.Store.AsOperator().WriteComputeLimits(t.Context(), teststore.OperatorAuthorized(t, e.Store, ""), map[string]int64{store.ComputeLimitNodesPerRun: 1}); err != nil {
		t.Fatal(err)
	}
	w := engineClaimHTTP(t, h, raw, http.MethodPost, "/api/v1/runs/run/plan", string(body), http.StatusUnprocessableEntity)
	var failure apierr.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || failure.Error.Code != apierr.PlanInvalid || !strings.Contains(w.Body.String(), store.ComputeLimitNodesPerRun) {
		t.Fatalf("plan limit refusal: %s: %v", w.Body, err)
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), "run")
	if err != nil || snapshot.Run.Spec != nil || len(snapshot.Nodes) != 0 {
		t.Fatalf("refused plan committed partial work: %+v: %v", snapshot, err)
	}
}
