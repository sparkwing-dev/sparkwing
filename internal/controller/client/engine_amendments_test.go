package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestEngineClientMixedClaimsUseOneQueue(t *testing.T) {
	input := controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}, Labels: []string{"linux"}}
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/v1/runners/claim" {
			t.Fatalf("queue = %s", req.URL.Path)
		}
		var received controllerwire.ClaimRequest
		if err := json.NewDecoder(req.Body).Decode(&received); err != nil || !reflect.DeepEqual(received, input) {
			t.Fatalf("claim changed: %+v %v", received, err)
		}
		return engineResponse(200, `{}`), nil
	})
	if _, err := c.Claim(t.Context(), input); err != nil {
		t.Fatal(err)
	}
}

func TestEngineClientRefusesInvalidClaimKinds(t *testing.T) {
	for _, launcher := range []bool{false, true} {
		for _, kinds := range [][]capability.UnitKind{nil, {"unknown"}, {capability.UnitNode, capability.UnitNode}} {
			c := testEngine(t, func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid claim reached transport")
				return nil, nil
			})
			c.launcher = launcher
			for _, run := range []string{"", "r1"} {
				_, err := c.Claim(t.Context(), controllerwire.ClaimRequest{Kinds: kinds, RunID: run})
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.BadRequest {
					t.Fatalf("launcher=%v kinds=%v run=%s: %v", launcher, kinds, run, err)
				}
			}
		}
	}
}

func TestEngineClientOneTimeAuthorityDoesNotReplay(t *testing.T) {
	here := controllerwire.CreateRunRequest{IdempotencyKey: "intake-key"}
	here.Request.Options.Runner.Set = true
	here.Request.Options.Runner.Value.Kind = runspec.RunnerHere
	for _, tt := range []struct {
		name string
		call func(controllerwire.Controller) error
	}{
		{"mixed-claim", func(c controllerwire.Controller) error {
			_, err := c.Claim(t.Context(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}})
			return err
		}},
		{"named-claim", func(c controllerwire.Controller) error {
			_, err := c.Claim(t.Context(), controllerwire.ClaimRequest{RunID: "r1", Kinds: []capability.UnitKind{capability.UnitTrigger}})
			return err
		}},
		{"named-job", func(c controllerwire.Controller) error {
			_, err := c.Claim(t.Context(), controllerwire.ClaimRequest{RunID: "r1", JobID: "j1", Kinds: []capability.UnitKind{capability.UnitNode}})
			return err
		}},
		{"reconcile", func(c controllerwire.Controller) error {
			_, err := c.ReconcileJob(t.Context(), "r1", "j1", controllerwire.ReconcileRequest{MaterializeOnly: true})
			return err
		}},
		{"exchange", func(c controllerwire.Controller) error { _, err := c.Exchange(t.Context()); return err }},
		{"here-intake", func(c controllerwire.Controller) error { _, err := c.CreateRun(t.Context(), here); return err }},
		{"retry-intake", func(c controllerwire.Controller) error {
			_, err := c.Retry(t.Context(), "r1", controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: "retry-key"})
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, unavailable := range []bool{false, true} {
				calls := 0
				c := testEngine(t, func(*http.Request) (*http.Response, error) {
					calls++
					if !unavailable {
						return nil, io.EOF
					}
					resp := engineResponse(503, `{"error":{"code":"unavailable","message":"retry"}}`)
					resp.Header.Set("Retry-After", "0")
					return resp, nil
				})
				if err := tt.call(c); err == nil || calls != 1 {
					t.Fatalf("one-time authority replayed: calls=%d err=%v", calls, err)
				}
			}
		})
	}
}

func TestEngineClientRecoveryAndLauncherMethods(t *testing.T) {
	failure := &nodeproto.JobFailure{Job: "source/build", Stage: "body", Error: &apierr.Error{Code: "future_error", Message: "masked", Details: map[string]any{"reason": "source"}}}
	for _, tt := range []struct {
		name, method, path string
		response           any
		call               func(controllerwire.Controller) (any, error)
	}{
		{"failure", "GET", "/api/v1/runs/r1/jobs/source%2Fbuild/failure?generation=3", failure, func(c controllerwire.Controller) (any, error) {
			return c.ReadFailure(t.Context(), "r1", "source/build", 3)
		}},
		{"cancel-job", "POST", "/api/v1/runs/r1/jobs/source%2Fbuild/cancel", controllerwire.Applied{CancelRequested: true, Attempt: 4, Generation: 3}, func(c controllerwire.Controller) (any, error) {
			return c.CancelJob(t.Context(), "r1", "source/build", controllerwire.CancelRequest{Reason: "owner cancelled"})
		}},
		{"launcher-sync", "POST", "/api/v1/launcher/sync", controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{}, Retired: []capability.LeaseRef{{Kind: capability.UnitNode, ID: "unit", Holder: "launcher", Generation: 1}}}, func(c controllerwire.Controller) (any, error) {
			return c.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{Observed: []capability.LauncherObservation{{Award: capability.LeaseRef{Kind: capability.UnitNode, ID: "unit-1", Holder: "launcher", Generation: 3}}}})
		}},
		{"exchange", "POST", "/api/v1/claims/exchange", controllerwire.ExchangeResponse{Claim: controllerwire.ClaimedUnit{RunID: "r1", Capability: "private-exchanged", Unit: capability.UnitNode}}, func(c controllerwire.Controller) (any, error) { return c.Exchange(t.Context()) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.response)
			if err != nil {
				t.Fatal(err)
			}
			c := testEngine(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != tt.method || req.URL.RequestURI() != tt.path {
					t.Fatalf("request = %s %s", req.Method, req.URL)
				}
				if tt.name == "exchange" {
					var input map[string]json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil || input == nil || len(input) != 0 || req.Header.Get("Content-Type") != "application/json" {
						t.Fatal("exchange requires an empty JSON object without caller-selected binding")
					}
				}
				if tt.name == "launcher-sync" {
					var input controllerwire.LauncherSyncRequest
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil || !reflect.DeepEqual(input.Observed, []capability.LauncherObservation{{Award: capability.LeaseRef{Kind: capability.UnitNode, ID: "unit-1", Holder: "launcher", Generation: 3}}}) {
						t.Fatalf("launcher observations changed: %+v %v", input, err)
					}
				}
				return engineResponse(200, string(body)), nil
			})
			got, err := tt.call(c)
			if err != nil || !reflect.DeepEqual(got, tt.response) {
				t.Fatalf("result changed: %+v %v", got, err)
			}
		})
	}
}

func TestEngineClientRefusesMalformedInnerErrors(t *testing.T) {
	for _, inner := range []string{`null`, `{"error":{"code":"forbidden","message":"nested"}}`, `{"code":"x"}`, `{"code":"x","message":null}`, `{"code":"x","code":"y","message":"duplicate"}`, `{"code":"x","message":"safe","details":{"a":1,"a":2}}`} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(403, `{"error":`+inner+`}`), nil })
		_, err := c.ReadRun(t.Context(), "r1")
		var failure *apierr.Error
		if err == nil || errors.As(err, &failure) {
			t.Fatalf("accepted malformed inner error %s: %v", inner, err)
		}
	}
}

func TestEngineClientReconcilePreservesExecutionState(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	job := controllerwire.ExecutionJob{
		JobDetail: controllerwire.JobDetail{ID: "build", Status: "running", Generation: 3, Attempt: 4, StateSeq: 18, Expansion: "accepted", StartedAt: &now, CancelRequestedAt: &now},
		Spec:      json.RawMessage(`{"id":"build","after":["source"]}`), SpecHash: "spec-hash", ClaimedBy: "granted-runner", CapabilityPrefix: "claim-prefix", ReadyAt: &now, LeaseExpiresAt: &now,
		ApplicationRetries: 3, Disruptions: 2, ExpansionDigest: "expansion-hash",
	}
	job.Execution.Retry.Value = 5
	job.Execution.Timeout.Value = time.Minute
	claim := &controllerwire.ClaimedUnit{
		Unit: capability.UnitNode, RunID: "r1", JobID: "build", UnitID: "unit-1", Capability: "private-award", Runner: nodeproto.RunnerIdentity{Name: "granted-runner", Type: "agent", Labels: []string{"linux"}},
		SourceGeneration: 3, OwnerDeadline: &now, AttemptDeadline: &now, ParentCapabilityExpiry: &now, LeaseExpiresAt: now,
		Engine: &engineid.Identity{Version: "v1.2.3", ManifestDigest: "verified-manifest"}, Execution: &job.Execution,
	}
	claim.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://example.com/team/repo.git", PipelineDir: "ci"}
	claim.Request.Request.Git.SHA = "sealed-sha"
	for _, kind := range []string{"dispatch", "execute"} {
		result := controllerwire.ReconcileResponse{Kind: kind, Job: job}
		if kind == "execute" {
			result.Claim = claim
		}
		body, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			var input controllerwire.ReconcileRequest
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil || !input.MaterializeOnly || input.Policy != "memo" {
				t.Fatalf("materialization request changed: %+v %v", input, err)
			}
			return engineResponse(200, string(body)), nil
		})
		got, err := c.ReconcileJob(t.Context(), "r1", "build", controllerwire.ReconcileRequest{Policy: "memo", MaterializeOnly: true})
		if err != nil || !reflect.DeepEqual(got, result) {
			t.Fatalf("persisted state changed: %+v %v", got, err)
		}
	}
}

func TestEngineClientExpansionReplayPreservesTopologyAndLifecycle(t *testing.T) {
	current := controllerwire.ExecutionJob{JobDetail: controllerwire.JobDetail{ID: "generated/build", Status: "finished", Outcome: "success", Generation: 2, Attempt: 3, StateSeq: 9, Expansion: "accepted"}, Spec: json.RawMessage(`{"id":"generated/build"}`), SpecHash: "accepted-spec", ApplicationRetries: 2, Disruptions: 1}
	expanded := controllerwire.ExpansionAccepted{SourceGeneration: 6, State: "accepted", BodyHash: "accepted-body", Replayed: true, Jobs: []controllerwire.ExpandedJob{{Job: nodeproto.JobSpec{ID: "generated/build"}, SpecHash: "accepted-spec", Current: current}}}
	snapshot := controllerwire.ScheduleSnapshot{RunID: "r1", Generators: []controllerwire.ScheduleGenerator{{ID: "empty", SourceJob: "source", State: "accepted", Members: []string{}}}, Expansions: []controllerwire.AcceptedExpansion{{SourceJob: "source", SourceGeneration: 6, Body: json.RawMessage(`{"jobs":[]}`), BodyHash: "empty-hash"}}}
	for _, tt := range []struct {
		result any
		call   func(controllerwire.Controller) (any, error)
	}{
		{expanded, func(c controllerwire.Controller) (any, error) {
			return c.AcceptExpansion(t.Context(), "r1", "source", controllerwire.ExpansionSubmission{SourceGeneration: 6, Body: json.RawMessage(`{"jobs":[]}`)})
		}},
		{snapshot, func(c controllerwire.Controller) (any, error) { return c.ReadSnapshot(t.Context(), "r1") }},
	} {
		body, err := json.Marshal(tt.result)
		if err != nil {
			t.Fatal(err)
		}
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, string(body)), nil })
		got, err := tt.call(c)
		if err != nil || !reflect.DeepEqual(got, tt.result) {
			t.Fatalf("expansion state changed: %+v %v", got, err)
		}
	}
}

func TestEngineClientLauncherSyncRefusesPrivateBearer(t *testing.T) {
	body, err := json.Marshal(controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{{Capability: "must-stay-private"}}})
	if err != nil {
		t.Fatal(err)
	}
	c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, string(body)), nil })
	result, err := c.SyncLauncher(t.Context(), controllerwire.LauncherSyncRequest{})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError || len(result.Awards) != 0 {
		t.Fatalf("sync exposed private award: %+v %v", result, err)
	}
}

func TestEngineClientRecoveryRejectsInvalidSelectorsAndInnerFailure(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid recovery selector reached transport")
		return nil, nil
	})
	for _, generation := range []int64{0, -1, -2} {
		if _, err := c.ReadFailure(t.Context(), "r1", "j1", generation); err == nil {
			t.Fatal("invalid generation accepted")
		}
	}
	if _, err := c.ReadFailure(t.Context(), ".", "j1", 1); err == nil {
		t.Fatal("invalid run accepted")
	}
	if _, err := c.CancelJob(t.Context(), "r1", "..", controllerwire.CancelRequest{}); err == nil {
		t.Fatal("invalid job accepted")
	}
	c = testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(200, `{"job":"j1","stage":"body","error":{"code":"failed","message":"safe","details":{"field":1,"field":2}}}`), nil
	})
	if _, err := c.ReadFailure(t.Context(), "r1", "j1", 1); err == nil {
		t.Fatal("malformed persisted failure accepted")
	}
}

func TestEngineClientRecoveryPreservesAbsentFailure(t *testing.T) {
	for _, body := range []string{"null", " \nnull\t"} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		got, err := c.ReadFailure(t.Context(), "r1", "j1", 2)
		if err != nil || got != nil {
			t.Fatalf("absent recorded failure: got=%+v error=%v", got, err)
		}
		if _, err := c.ReadRun(t.Context(), "r1"); err == nil {
			t.Fatal("public run read accepted null")
		}
	}
	for _, body := range []string{"", "{}", "null null", "[]", "false", `{"job":"j1","stage":"body","error":null}`} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.ReadFailure(t.Context(), "r1", "j1", 2); err == nil {
			t.Fatalf("accepted malformed failure %q", body)
		}
	}
}
