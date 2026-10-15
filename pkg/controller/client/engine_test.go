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

type engineTransport func(*http.Request) (*http.Response, error)

func (f engineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func engineResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func testEngine(t *testing.T, transport engineTransport) *engineClient {
	t.Helper()
	c, err := NewEngineClient(EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: transport}, Bearer: func(context.Context) (string, error) { return "private-capability", nil }})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*engineClient)
}

func TestEngineClientRoutes(t *testing.T) {
	ctx := t.Context()
	tests := []struct {
		name, method, path string
		result             any
		call               func(controllerwire.Controller) (any, error)
	}{
		{"compatibility", "GET", "/api/v1/capabilities?engine_version=v1.2.3", controllerwire.Compatibility{APIMajor: 1, EngineRelease: "v1.2.3"}, func(c controllerwire.Controller) (any, error) { return c.Compatibility(ctx, "v1.2.3") }},
		{"create", "POST", "/api/v1/runs", controllerwire.CreateRunResponse{RunID: "r1", Number: 42, Replayed: true}, func(c controllerwire.Controller) (any, error) {
			return c.CreateRun(ctx, controllerwire.CreateRunRequest{IdempotencyKey: "key"})
		}},
		{"read", "GET", "/api/v1/runs/r1", controllerwire.RunDetail{ID: "r1", Number: 42, Status: "running", Jobs: []controllerwire.JobDetail{{ID: "j1", Status: "running", Attempt: 2, Generation: 3, StateSeq: 4}}}, func(c controllerwire.Controller) (any, error) { return c.ReadRun(ctx, "r1") }},
		{"snapshot", "GET", "/api/v1/runs/r1/snapshot", controllerwire.ScheduleSnapshot{RunID: "r1", Generators: []controllerwire.ScheduleGenerator{{ID: "g1", Members: []string{}}}}, func(c controllerwire.Controller) (any, error) { return c.ReadSnapshot(ctx, "r1") }},
		{"plan", "POST", "/api/v1/runs/r1/plan", controllerwire.PlanAccepted{Replayed: true}, func(c controllerwire.Controller) (any, error) {
			return c.AcceptPlan(ctx, "r1", controllerwire.PlanSubmission{})
		}},
		{"claim", "POST", "/api/v1/runners/claim", controllerwire.ClaimResponse{}, func(c controllerwire.Controller) (any, error) {
			return c.Claim(ctx, controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitNode}})
		}},
		{"renew", "POST", "/api/v1/claims/c1/heartbeat", controllerwire.RenewalResponse{CancelRequested: true, CancelReason: "cancel", WatchedJobs: []string{"j1"}}, func(c controllerwire.Controller) (any, error) {
			return c.Renew(ctx, "c1", controllerwire.RenewalRequest{Executing: true})
		}},
		{"transition", "POST", "/api/v1/runs/r1/jobs/j1/transition", controllerwire.Applied{StateSeq: 5, Attempt: 2, Generation: 3, Replayed: true}, func(c controllerwire.Controller) (any, error) {
			return c.TransitionJob(ctx, "r1", "j1", controllerwire.JobTransitionRequest{To: "finished"})
		}},
		{"reconcile", "POST", "/api/v1/runs/r1/jobs/j1/reconcile", controllerwire.ReconcileResponse{Kind: "execute", Job: controllerwire.ExecutionJob{JobDetail: controllerwire.JobDetail{ID: "j1"}}}, func(c controllerwire.Controller) (any, error) {
			return c.ReconcileJob(ctx, "r1", "j1", controllerwire.ReconcileRequest{})
		}},
		{"expand", "POST", "/api/v1/runs/r1/jobs/j1/expansion", controllerwire.ExpansionAccepted{SourceGeneration: 3, State: "succeeded", Jobs: []controllerwire.ExpandedJob{}}, func(c controllerwire.Controller) (any, error) {
			return c.AcceptExpansion(ctx, "r1", "j1", controllerwire.ExpansionSubmission{SourceGeneration: 3, Body: json.RawMessage(`{"jobs":[]}`)})
		}},
		{"finish", "POST", "/api/v1/runs/r1/finish", controllerwire.Applied{StateSeq: 6}, func(c controllerwire.Controller) (any, error) {
			return c.Finish(ctx, "r1", controllerwire.FinishRequest{Outcome: "success"})
		}},
		{"cancel", "POST", "/api/v1/runs/r1/cancel", controllerwire.Applied{CancelRequested: true}, func(c controllerwire.Controller) (any, error) {
			return c.Cancel(ctx, "r1", controllerwire.CancelRequest{Reason: "stop"})
		}},
		{"retry", "POST", "/api/v1/runs/r1/retry", controllerwire.CreateRunResponse{RunID: "r2", Number: 43}, func(c controllerwire.Controller) (any, error) {
			return c.Retry(ctx, "r1", controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: "key"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.result)
			if err != nil {
				t.Fatal(err)
			}
			c := testEngine(t, func(r *http.Request) (*http.Response, error) {
				if r.Method != tt.method || r.URL.RequestURI() != tt.path {
					t.Fatalf("request = %s %s", r.Method, r.URL.RequestURI())
				}
				if r.Header.Get("Authorization") != "Bearer private-capability" || r.Header.Get("X-Sparkwing-Node-Protocol") != "" {
					t.Fatalf("unexpected headers: %v", r.Header)
				}
				return engineResponse(200, string(body)), nil
			})
			got, err := tt.call(c)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(got)
			if err != nil || string(encoded) != string(body) {
				t.Fatalf("detail changed during transport: %v", err)
			}
		})
	}
}

func TestEngineClientErrorsAndRetry(t *testing.T) {
	for _, code := range []apierr.Code{apierr.LockHeld, apierr.IllegalTransition, apierr.Forbidden, apierr.Unauthenticated, "future_code", apierr.Unavailable} {
		t.Run(string(code), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				c := testEngine(t, func(r *http.Request) (*http.Response, error) {
					calls++
					resp := engineResponse(apierr.Status(code), `{"error":{"code":"`+string(code)+`","message":"safe","hint":"next","details":{"reason":"binding"}},"future":true}`)
					resp.Header.Set("Retry-After", "1")
					return resp, nil
				})
				_, err := c.Renew(t.Context(), "c1", controllerwire.RenewalRequest{})
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != code || failure.Hint != "next" {
					t.Fatalf("error lost: %v", err)
				}
				want := 1
				if code == apierr.Unavailable {
					want = unavailableRetries + 1
				}
				if calls != want {
					t.Fatalf("calls=%d want=%d", calls, want)
				}
			})
		})
	}
}

func TestEngineClientExactRetryAndIdempotency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var bodies, keys []string
		c := testEngine(t, func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, string(body))
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			if len(bodies) == 1 {
				return nil, io.EOF
			}
			encoded, err := json.Marshal(controllerwire.CreateRunResponse{RunID: "r1", Replayed: true})
			if err != nil {
				t.Fatal(err)
			}
			return engineResponse(200, string(encoded)), nil
		})
		got, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{IdempotencyKey: "stable-key"})
		if err != nil || !got.Replayed || len(bodies) != 2 || bodies[0] != bodies[1] || !reflect.DeepEqual(keys, []string{"stable-key", "stable-key"}) {
			t.Fatalf("replay changed: %#v %v %v", got, err, keys)
		}
		bodies, keys = nil, nil
		_, err = c.CreateRun(t.Context(), controllerwire.CreateRunRequest{})
		if err == nil || len(bodies) != 1 {
			t.Fatalf("unkeyed intake retried: %v (%d)", err, len(bodies))
		}
	})
}

func TestEngineClientRefusesMalformedResponsesAndRedirects(t *testing.T) {
	for _, body := range []string{`null`, `{"CLAIM":{}}`, `{"claim":null,"CLAIM":{}}`, `{"claim":false}`, `{} {}`, `{"claim":null,"claim":null}`} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.Claim(t.Context(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitNode}}); err == nil {
			t.Fatalf("accepted malformed response %s", body)
		}
	}
	calls := 0
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		calls++
		r := engineResponse(307, "")
		r.Header.Set("Location", "http://foreign/steal")
		return r, nil
	})
	if _, err := c.ReadRun(t.Context(), "r1"); err == nil || calls != 1 {
		t.Fatalf("redirect followed: %v (%d)", err, calls)
	}
}

func TestEngineClientRequiredFieldsAndAdditions(t *testing.T) {
	fixture, err := json.Marshal(controllerwire.RunDetail{ID: "r1", Number: 42, Status: "success"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &fields); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name  string
		value json.RawMessage
	}{
		{"id", nil}, {"number", json.RawMessage(`null`)},
		{"jobs", json.RawMessage(`[{"id":"j1"}]`)},
		{"inputs", json.RawMessage(`null`)},
	} {
		original := fields[change.name]
		fields[change.name] = change.value
		if change.value == nil {
			delete(fields, change.name)
		}
		body, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, string(body)), nil })
		if _, err := c.ReadRun(t.Context(), "r1"); err == nil {
			t.Fatalf("accepted invalid %s", change.name)
		}
		fields[change.name] = original
	}
	fields["future_optional_field"] = json.RawMessage(`{"a":[true]}`)
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, string(body)), nil })
	got, err := c.ReadRun(t.Context(), "r1")
	if err != nil || got.ID != "r1" {
		t.Fatalf("additive field refused: %v", err)
	}
}

func TestEngineClientNamedClaimsAndSelectorEscaping(t *testing.T) {
	for _, tt := range []struct {
		request controllerwire.ClaimRequest
		path    string
	}{
		{controllerwire.ClaimRequest{RunID: "here-1", Kinds: []capability.UnitKind{capability.UnitTrigger}}, "/api/v1/runs/here-1/claim"},
		{controllerwire.ClaimRequest{RunID: "r1", JobID: "group/build", Kinds: []capability.UnitKind{capability.UnitNode}}, "/api/v1/runs/r1/jobs/group%2Fbuild/claim"},
		{controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitTrigger}}, "/api/v1/runners/claim"},
	} {
		c := testEngine(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.EscapedPath() != tt.path || r.Header.Get("Idempotency-Key") != "" {
				t.Fatalf("wrong claim: %s", r.URL)
			}
			return engineResponse(200, `{}`), nil
		})
		if _, err := c.Claim(t.Context(), tt.request); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"", ".", "..", "r\n1"} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid selector reached transport")
			return nil, nil
		})
		if _, err := c.ReadRun(t.Context(), id); err == nil {
			t.Fatalf("accepted selector %q", id)
		}
	}
}

func TestEngineClientLauncherSelection(t *testing.T) {
	for _, input := range []controllerwire.ClaimRequest{
		{Kinds: []capability.UnitKind{capability.UnitNode}},
		{Kinds: []capability.UnitKind{capability.UnitNode, capability.UnitPlan, capability.UnitEval}},
		{Kinds: []capability.UnitKind{capability.UnitNode}, RunID: "r1", JobID: "j1"},
	} {
		c, err := NewEngineClient(EngineOptions{URL: "http://controller", Launcher: true,
			HTTP: &http.Client{Transport: engineTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/api/v1/launcher/claim" {
					t.Fatalf("launcher selected %s", r.URL.Path)
				}
				var received controllerwire.ClaimRequest
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(received, input) {
					t.Fatal("launcher selection changed")
				}
				return engineResponse(200, `{}`), nil
			})}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Claim(t.Context(), input); err != nil {
			t.Fatal(err)
		}
	}
}
