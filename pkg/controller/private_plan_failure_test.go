package controller

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func privatePlanFailureFixture(t *testing.T) (*EngineController, *store.Tenant, string) {
	t.Helper()
	e, tenant := engineRuntimeStore(t)
	engineSQL(t, e.Store, `UPDATE runs SET dispatch='controller'`)
	engineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan'`)
	return e, tenant, LauncherFixtureClaim(t, e.Store, "run", store.PlanNodeID)
}

func TestPrivatePlanFailureClosesPlanAndRunWithBoundedStage(t *testing.T) {
	for _, outcome := range []store.RunState{store.RunFailed, store.RunCancelled} {
		t.Run(string(outcome), func(t *testing.T) {
			e, tenant, raw := privatePlanFailureFixture(t)
			message, reason := "run preparation failed", string(apierr.PlanFailed)
			if outcome == store.RunCancelled {
				message, reason = "cancellation requested", string(apierr.ReasonCancelled)
			}
			body, err := json.Marshal(localhostwire.LocalRunTransition{To: outcome, Cause: store.RunFinish, Reason: "untrusted-stage", MaskedError: "unmasked-secret"})
			if err != nil {
				t.Fatal(err)
			}
			h := New(e.Store, nil).Handler()
			for range 2 {
				engineClaimHTTP(t, h, raw, http.MethodPost, "/api/v1/runs/run/local-host/finish", string(body), http.StatusOK)
			}
			row, err := tenant.ReadRun(t.Context(), "run")
			if err != nil || row.State != outcome || row.FinishedAt == nil {
				t.Fatalf("plan failure did not finish run: state=%s %v", row.State, err)
			}
			var status, errorText string
			var until sql.NullInt64
			if err := e.Store.DB().QueryRowContext(t.Context(), `SELECT t.status,t.lease_expires_at,r.error FROM triggers t JOIN runs r ON r.team=t.team AND r.id=t.id WHERE t.id='run'`).Scan(&status, &until, &errorText); err != nil || status != "done" || until.Valid || errorText != message {
				t.Fatalf("plan completion status=%s lease=%v error=%q: %v", status, until, errorText, err)
			}
			events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 10)
			if err != nil || len(events) != 1 || !strings.Contains(string(events[0].Payload), reason) {
				t.Fatalf("completion events=%+v: %v", events, err)
			}
		})
	}
}

func FuzzPlanFailureCannotEscapeUnacceptedLease(f *testing.F) {
	for mutation := range uint8(6) {
		f.Add(mutation, "unmasked-stage")
	}
	f.Fuzz(func(t *testing.T, mutation uint8, message string) {
		e, tenant, raw := privatePlanFailureFixture(t)
		body := localhostwire.LocalRunTransition{To: store.RunFailed, Cause: store.RunFinish, MaskedError: message}
		path := "/api/v1/runs/run/local-host/finish"
		switch mutation % 6 {
		case 0:
			body.To = store.RunSuccess
		case 1:
			body.Cause = store.RunStart
		case 2:
			engineSQL(t, e.Store, `UPDATE runs SET spec_json=?,spec_hash='accepted',plan_json=?,plan_hash='plan',sealed_at=1`, []byte(`{}`), []byte(`{"jobs":[]}`))
		case 3:
			engineSQL(t, e.Store, `UPDATE triggers SET generation=generation+1`)
		case 4:
			path = "/api/v1/runs/foreign/local-host/finish"
		case 5:
			body.OwnerPolicy = &store.OwnerPolicyResult{}
		}
		before, err := tenant.ReadSnapshot(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		principal, err := e.Store.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
		if err != nil || principal.Capability.Unit != capability.UnitPlan {
			t.Fatalf("test did not use a persisted plan: %v", err)
		}
		binding := principal.Capability.Binding
		actor := store.Actor{Kind: store.ActorRunHost, Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix, Holder: binding.Holder, Generation: binding.Generation, Owner: store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: binding.RunID, Holder: binding.Holder, Generation: binding.Generation}}
		change := store.RunTransition{RunID: "run", Actor: actor, To: body.To, Cause: body.Cause, OwnerPolicy: body.OwnerPolicy, MaskedError: body.MaskedError}
		if mutation%6 == 4 {
			change.RunID = "foreign"
		}
		if _, err := tenant.FinishRunAndUnit(t.Context(), store.FinishRunRequest{Unit: actor.Owner, Transition: change}); err == nil {
			t.Fatal("direct store call escaped plan failure authority")
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(encoded)))
		request.Header.Set("Authorization", "Bearer "+raw)
		response := httptest.NewRecorder()
		New(e.Store, nil).Handler().ServeHTTP(response, request)
		if response.Code < 400 {
			t.Fatalf("plan escaped its failure authority: mutation=%d status=%d", mutation%6, response.Code)
		}
		after, err := tenant.ReadSnapshot(t.Context(), "run")
		before.StoreNow = after.StoreNow
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("refused plan changed run: %v", err)
		}
	})
}
