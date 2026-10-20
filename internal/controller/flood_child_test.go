package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestChildrenSpendTheirRootsFloodBudget(t *testing.T) {
	for _, app := range []bool{false, true} {
		for _, queue := range []bool{false, true} {
			t.Run(fmt.Sprintf("App=%v/queue=%v", app, queue), func(t *testing.T) {
				e, _, claim := childIntakeFixture(t)
				tenant, err := e.Store.ForTeam(t.Context(), "a")
				if err != nil {
					t.Fatal(err)
				}
				root, err := tenant.ReadRun(t.Context(), "run")
				if err != nil {
					t.Fatal(err)
				}
				root.Request.AuthorityRef = capability.AuthorityRef{Kind: "schedule", ID: "original-schedule"}
				if app {
					root.Request.Request.Origin = runspec.OriginApp
					root.Request.AuthorityRef = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "app-original"}
				}
				body, err := json.Marshal(root.Request)
				if err != nil {
					t.Fatal(err)
				}
				engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, body)
				s := New(e.Store, nil).WithEngineController(e).WithFloodPolicy(FloodPolicy{RunsPerPrincipalHour: 1})
				// Spend the root's token through the same accounting used by root admission.
				principal := capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "retrier"}, Capability: capability.Capability{Team: "a"}}
				intake := store.IntakeRequest{Authority: principal.Authority}
				engineSQL(t, e.Store, `UPDATE runs SET lineage_json=? WHERE team='a' AND id='run'`, `{"admission_key":"capability:retrier","admission_subject":{"kind":"account","id":"retrier"}}`)
				if _, err := admitWithFlood(t.Context(), s, func(ctx context.Context) (bool, error) {
					_, err := s.admissionFloodRefusal(ctx, nil, intake, principal)
					return true, err
				}); err != nil {
					t.Fatal(err)
				}
				want := http.StatusTooManyRequests
				if queue {
					s.WithFloodPolicy(FloodPolicy{ShedQueueDepth: 1})
					engineSQL(t, e.Store, `UPDATE triggers SET status='pending' WHERE team='a' AND id='run'`)
					want = http.StatusServiceUnavailable
				}
				engineClaimHTTP(t, s.Handler(), claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "deploy", root.Request.Request.Git), want)
				var count int
				if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("refused child wrote a run: %d %v", count, err)
				}
			})
		}
	}
}

func TestRetryRecordsAdmissionIdentitySeparatelyFromSource(t *testing.T) {
	for _, origin := range []runspec.Origin{runspec.OriginSchedule, runspec.OriginApp} {
		t.Run(string(origin), func(t *testing.T) {
			e, _, _ := childIntakeFixture(t)
			tenant, err := e.Store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			source, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			source.Request.Request.Origin = origin
			source.Request.AuthorityRef = capability.AuthorityRef{Kind: "schedule", ID: "original"}
			if origin == runspec.OriginApp {
				source.Request.AuthorityRef = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "app-original"}
			}
			source.Request.Hash, err = runspec.CanonicalHash(source.Request)
			if err != nil {
				t.Fatal(err)
			}
			body := sealWith(t, source.Request, source.Request.Provenance)
			if err := json.Unmarshal(body, &source.Request); err != nil {
				t.Fatal(err)
			}
			if source.Spec != nil {
				source.Spec.Request = source.Request
				source.Spec.Hash, err = runspec.CanonicalHash(*source.Spec)
				if err != nil {
					t.Fatal(err)
				}
				spec, err := json.Marshal(source.Spec)
				if err != nil {
					t.Fatal(err)
				}
				engineSQL(t, e.Store, `UPDATE runs SET spec_json=? WHERE team='a' AND id='run'`, spec)
			}
			engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, body)
			if err := teststore.SeedRunFinish(e.Store, t.Context(), "a", "run", "failed", "boom"); err != nil {
				t.Fatal(err)
			}
			raw, retrier, err := teststore.ProvisionCredential(t.Context(), e.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "retrier"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsControl, capability.RunsRead, capability.RunsWrite}})
			if err != nil {
				t.Fatal(err)
			}
			s := New(e.Store, nil).WithEngineController(e).WithFloodPolicy(FloodPolicy{RunsPerPrincipalHour: 1})
			response := engineClaimHTTP(t, s.Handler(), raw, "POST", "/api/v1/runs/run/retry", `{"mode":"all","idempotency_key":"retry"}`, http.StatusOK)
			var result struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			retry, err := tenant.ReadRun(t.Context(), result.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var lineage []byte
			if err := teststore.SQL(t, e.Store).QueryRow(`SELECT lineage_json FROM runs WHERE team='a' AND id=?`, retry.ID).Scan(&lineage); err != nil {
				t.Fatal(err)
			}
			var charged struct {
				Subject capability.Subject `json:"admission_subject"`
				Key     string             `json:"admission_key"`
			}
			if err := json.Unmarshal(lineage, &charged); err != nil {
				t.Fatal(err)
			}
			if charged.Subject != retrier.Subject || charged.Key != "capability:"+retrier.Prefix {
				t.Fatalf("retry charged another identity: %+v", charged)
			}
			if retry.Request.AuthorityRef != source.Request.AuthorityRef || retry.Request.Request.Origin != origin {
				t.Fatal("retry changed original provenance")
			}
		})
	}
}

func TestAppAndScheduleAdmissionsShareTeamBudget(t *testing.T) {
	s := New(nil, nil).WithFloodPolicy(FloodPolicy{RunsPerPrincipalHour: 1})
	principal := capability.Principal{Authority: capability.AuthorityRef{Kind: "schedule", ID: "schedule"}, Capability: capability.Capability{Team: "a"}}
	admit := func(request store.IntakeRequest, p capability.Principal) error {
		_, err := admitWithFlood(t.Context(), s, func(ctx context.Context) (bool, error) {
			_, err := s.admissionFloodRefusal(ctx, nil, request, p)
			return true, err
		})
		return err
	}
	if err := admit(store.IntakeRequest{}, principal); err != nil {
		t.Fatal(err)
	}
	principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "app"}
	if err := admit(store.IntakeRequest{VerifiedDeliveryID: "verified"}, principal); err == nil {
		t.Fatal("App admission escaped schedule's team budget")
	}
}

func TestChildFloodAdmissionRequiresRecordedIdentity(t *testing.T) {
	e, _, claim := childIntakeFixture(t)
	engineSQL(t, e.Store, `UPDATE runs SET lineage_json=? WHERE team='a' AND id='run'`, []byte(`{"admission_subject":{"kind":"account","id":"fixture"}}`))
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	root, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	s := New(e.Store, nil).WithEngineController(e).WithFloodPolicy(FloodPolicy{RunsPerPrincipalHour: 1})
	engineClaimHTTP(t, s.Handler(), claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "deploy", root.Request.Request.Git), http.StatusUnprocessableEntity)
}
