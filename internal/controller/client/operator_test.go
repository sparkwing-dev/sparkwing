package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestOperatorTeamCreationNeverReplaysOneTimeCredential(t *testing.T) {
	for _, failure := range []string{"unavailable", "lost response"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			manager, err := NewOperatorManager(EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: engineTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.GetBody != nil || r.Method != http.MethodPost || r.URL.Path != "/api/v1/operator/teams" {
					t.Fatalf("unsafe request: %+v", r)
				}
				if failure == "lost response" {
					return nil, io.ErrUnexpectedEOF
				}
				response := engineResponse(503, `{"error":{"code":"unavailable","message":"busy"}}`)
				response.Header.Set("Retry-After", "1")
				return response, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.CreateOperatorTeam(t.Context(), controllerwire.OperatorTeamCreate{Slug: "test-team"}); err == nil || calls != 1 {
				t.Fatalf("failure=%v calls=%d", err, calls)
			}
		})
	}
}

func TestOperatorTeamCreationRequiresCompleteCredential(t *testing.T) {
	for _, field := range []string{"team", "account", "capability", "prefix", "grants"} {
		t.Run(field, func(t *testing.T) {
			created := controllerwire.OperatorTeamCreated{Team: "test-team", AccountID: "account", Capability: "one-time-credential", Prefix: "public-prefix", Grants: []capability.Grant{capability.TeamAdmin}}
			switch field {
			case "team":
				created.Team = "foreign"
			case "account":
				created.AccountID = ""
			case "capability":
				created.Capability = ""
			case "prefix":
				created.Prefix = ""
			case "grants":
				created.Grants = nil
			}
			manager, err := NewOperatorManager(EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) { return operatorResponse(t, created), nil })}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = manager.CreateOperatorTeam(t.Context(), controllerwire.OperatorTeamCreate{Slug: "test-team"})
			var api *apierr.Error
			if !errors.As(err, &api) || api.Code != apierr.ProtocolError {
				t.Fatalf("incomplete %s: %v", field, err)
			}
		})
	}
}

func operatorResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return engineResponse(http.StatusOK, string(body))
}

func TestOperatorManagementUsesDeclaredRoutes(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		limits := controllerwire.OperatorLimits{Limits: map[string]int64{"max_jobs_per_run": 0}}
		state := controllerwire.ObjectStoreBreaker{Classes: []controllerwire.ObjectStoreClassState{}, Ceiling: controllerwire.ObjectStoreCeiling{Incomplete: true, MeasureError: "measurement failed"}}
		calls := 0
		manager, err := NewOperatorManager(EngineOptions{URL: endpoint, Bearer: func(_ context.Context) (string, error) { return "deployment-token", nil }, HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.Header.Get("Authorization") != "Bearer deployment-token" {
				t.Fatal("missing credential")
			}
			switch calls {
			case 1:
				if req.Method != http.MethodGet || req.URL.Path != "/api/v1/operator/limits" {
					t.Fatalf("read limits: %s %s", req.Method, req.URL)
				}
				return operatorResponse(t, limits), nil
			case 2:
				var body controllerwire.OperatorLimitsWrite
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if req.Method != http.MethodPut || req.URL.Path != "/api/v1/operator/limits" || !reflect.DeepEqual(body.Limits, limits.Limits) {
					t.Fatalf("write limits: %s %s %+v", req.Method, req.URL, body)
				}
				return operatorResponse(t, limits), nil
			case 3:
				if req.Method != http.MethodGet || req.URL.Path != "/api/v1/operator/object-store/breaker" {
					t.Fatalf("read breaker: %s %s", req.Method, req.URL)
				}
				return operatorResponse(t, state), nil
			case 4:
				if req.Method != http.MethodPost || req.URL.Path != "/api/v1/operator/object-store/reset-breaker" || req.Header.Get("Idempotency-Key") != "stable" {
					t.Fatalf("reset breaker: %s %s", req.Method, req.URL)
				}
				return operatorResponse(t, state), nil
			default:
				t.Fatal("unexpected request")
				return nil, io.EOF
			}
		})}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := manager.ReadOperatorLimits(t.Context())
		if err != nil || !reflect.DeepEqual(got.Limits, limits.Limits) {
			t.Fatalf("limits changed: %+v %v", got, err)
		}
		got, err = manager.WriteOperatorLimits(t.Context(), controllerwire.OperatorLimitsWrite{Limits: limits.Limits})
		if err != nil || !reflect.DeepEqual(got.Limits, limits.Limits) {
			t.Fatalf("write changed: %+v %v", got, err)
		}
		for _, reset := range []bool{false, true} {
			var got controllerwire.ObjectStoreBreaker
			if reset {
				got, err = manager.ResetObjectStoreBreaker(t.Context(), "stable")
			} else {
				got, err = manager.ReadObjectStoreBreaker(t.Context())
			}
			if err != nil || !reflect.DeepEqual(got, state) {
				t.Fatalf("measurement changed: %+v %v", got, err)
			}
		}
		if calls != 4 {
			t.Fatalf("calls = %d", calls)
		}
	}
}

func TestOperatorManagementRejectsUnconfirmedWrites(t *testing.T) {
	for _, current := range []map[string]int64{nil, {}, {"max_jobs_per_run": 2}, {"other": 0}} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) {
			return operatorResponse(t, controllerwire.OperatorLimits{Limits: current}), nil
		})
		if _, err := c.WriteOperatorLimits(t.Context(), controllerwire.OperatorLimitsWrite{Limits: map[string]int64{"max_jobs_per_run": 0}}); err == nil {
			t.Fatalf("unconfirmed setting accepted: %v", current)
		}
	}
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid request reached HTTP")
		return nil, io.EOF
	})
	for _, values := range []map[string]int64{nil, {}, {"": 1}, {"max_jobs_per_run": -1}} {
		if _, err := c.WriteOperatorLimits(t.Context(), controllerwire.OperatorLimitsWrite{Limits: values}); err == nil {
			t.Fatalf("invalid setting accepted: %v", values)
		}
	}
	if _, err := c.ResetObjectStoreBreaker(t.Context(), ""); err == nil {
		t.Fatal("reset without replay identity accepted")
	}
}

func TestOperatorManagementKeepsCanonicalRefusals(t *testing.T) {
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		return engineResponse(403, `{"error":{"code":"forbidden","message":"deployment scope required","hint":"select a deployment credential","details":{"scope":"operator.limits"}}}`), nil
	})
	_, err := c.ReadOperatorLimits(t.Context())
	var canonical *apierr.Error
	if !errors.As(err, &canonical) || canonical.Code != apierr.Forbidden || canonical.Hint != "select a deployment credential" || !strings.Contains(err.Error(), "deployment scope") {
		t.Fatalf("refusal changed: %v", err)
	}
}

func TestOperatorManagementRetriesKeepExactMutation(t *testing.T) {
	for _, reset := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var bodies, keys []string
			c := testEngine(t, func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				bodies = append(bodies, string(body))
				keys = append(keys, req.Header.Get("Idempotency-Key"))
				if len(bodies) == 1 {
					return nil, io.EOF
				}
				if reset {
					return operatorResponse(t, controllerwire.ObjectStoreBreaker{}), nil
				}
				return operatorResponse(t, controllerwire.OperatorLimits{Limits: map[string]int64{"max_jobs_per_run": 0}}), nil
			})
			var err error
			if reset {
				_, err = c.ResetObjectStoreBreaker(t.Context(), "same-reset")
			} else {
				_, err = c.WriteOperatorLimits(t.Context(), controllerwire.OperatorLimitsWrite{Limits: map[string]int64{"max_jobs_per_run": 0}})
			}
			if err != nil || len(bodies) != 2 || bodies[0] != bodies[1] || keys[0] != keys[1] || reset && keys[0] != "same-reset" {
				t.Fatalf("mutation replay changed: bodies=%v keys=%v error=%v", bodies, keys, err)
			}
		})
	}
}

func TestOperatorManagementRejectsMalformedResponses(t *testing.T) {
	for _, field := range []string{"limits", "usage", "budgets"} {
		fixture, err := json.Marshal(controllerwire.OperatorLimits{Limits: map[string]int64{"max_jobs_per_run": 0}})
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(fixture, &object); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`false`)} {
			object[field] = invalid
			if invalid == nil {
				delete(object, field)
			}
			body, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, string(body)), nil })
			if _, err := c.ReadOperatorLimits(t.Context()); err == nil {
				t.Fatalf("invalid %s accepted", field)
			}
		}
	}
}

func FuzzOperatorMutationsStayOnDeclaredRoutes(f *testing.F) {
	for _, name := range []string{"max_jobs_per_run", "../operator/refunds", "x\nAuthorization: forged", "", " future_guard ", "max_global_runners"} {
		f.Add(name, int64(0), false)
		f.Add(name, int64(7), true)
	}
	f.Fuzz(func(t *testing.T, name string, value int64, reset bool) {
		calls := 0
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			calls++
			if req.Header.Get("Authorization") != "Bearer private-capability" || req.URL.RawQuery != "" || req.URL.Fragment != "" {
				t.Fatal("input changed request authority")
			}
			if reset {
				if req.Method != http.MethodPost || req.URL.Path != "/api/v1/operator/object-store/reset-breaker" || req.Header.Get("Idempotency-Key") != "one-reset" {
					t.Fatal("input selected another reset operation")
				}
				return operatorResponse(t, controllerwire.ObjectStoreBreaker{}), nil
			}
			if req.Method != http.MethodPut || req.URL.Path != "/api/v1/operator/limits" {
				t.Fatal("input selected another limits operation")
			}
			return operatorResponse(t, controllerwire.OperatorLimits{Limits: map[string]int64{name: value}}), nil
		})
		if reset {
			if _, err := c.ResetObjectStoreBreaker(t.Context(), "one-reset"); err != nil {
				t.Fatal(err)
			}
		} else {
			input := controllerwire.OperatorLimitsWrite{Limits: map[string]int64{name: value}}
			_, err := c.WriteOperatorLimits(t.Context(), input)
			if input.Validate() != nil && (err == nil || calls != 0) {
				t.Fatal("invalid input reached transport")
			}
		}
		if calls > 1 {
			t.Fatal("input expanded to more than one operation")
		}
	})
}
