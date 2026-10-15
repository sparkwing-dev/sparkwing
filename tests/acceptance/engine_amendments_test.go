package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestEngineAmendmentsPreservePrivateWireContract(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			want := controllerwire.ReconcileResponse{Kind: "execute", Job: controllerwire.ExecutionJob{JobDetail: controllerwire.JobDetail{ID: "build", Status: "running", Generation: 3, Attempt: 4, StateSeq: 19, Expansion: "accepted", CancelRequestedAt: &now}, Spec: json.RawMessage(`{"id":"build"}`), SpecHash: "spec-hash", ApplicationRetries: 2, Disruptions: 1, ClaimedBy: "granted-runner", LeaseExpiresAt: &now}, Claim: &controllerwire.ClaimedUnit{Capability: "private-award", SourceGeneration: 3, OwnerDeadline: &now, Engine: &engine.Identity{Version: "v1.2.3", ManifestDigest: "verified-manifest"}}}
			want.Claim.Runner.Name = "granted-runner"
			want.Claim.Request.Request.Git.SHA = "sealed-sha"
			want.Claim.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: "https://example.com/team/repo.git", PipelineDir: "ci"}
			malformed, calls := false, 0
			transport := surfaceTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("Authorization") != "Bearer private-controller" {
					t.Fatal("controller authority missing")
				}
				switch req.URL.Path {
				case "/api/v1/runners/claim", "/api/v1/launcher/claim":
					var input controllerwire.ClaimRequest
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					kinds := []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}
					if req.URL.Path == "/api/v1/launcher/claim" {
						kinds = []capability.UnitKind{capability.UnitNode}
					}
					if req.Method != "POST" || !reflect.DeepEqual(input.Kinds, kinds) {
						t.Fatalf("claim changed: %+v", input)
					}
					return engineContractResponse(t, controllerwire.ClaimResponse{}), nil
				case "/api/v1/runs/r1/jobs/source/failure":
					if req.Method != "GET" || req.URL.Query().Get("generation") != "3" {
						t.Fatalf("failure selector: %s %s", req.Method, req.URL)
					}
					inner := `{"code":"future_error","message":"masked","hint":"recover","future":true}`
					if malformed {
						inner = `{"code":"x","code":"y","message":"duplicate"}`
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"job":"source","stage":"body","error":` + inner + `}`))}, nil
				case "/api/v1/runs/r1/jobs/build/reconcile":
					var input controllerwire.ReconcileRequest
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil || input.MaterializeOnly || input.Policy != "resume" {
						t.Fatalf("reconciliation changed: %+v %v", input, err)
					}
					return engineContractResponse(t, want), nil
				default:
					t.Fatalf("unexpected request: %s", req.URL)
					return nil, nil
				}
			})
			options := client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: transport}, Bearer: func(context.Context) (string, error) { return "private-controller", nil }}
			c, err := client.NewEngineClient(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Claim(t.Context(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}}); err != nil {
				t.Fatal(err)
			}
			options.Launcher = true
			launcher, err := client.NewEngineClient(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := launcher.Claim(t.Context(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitNode}}); err != nil {
				t.Fatal(err)
			}
			failure, err := c.ReadFailure(t.Context(), "r1", "source", 3)
			if err != nil || failure.Error.Code != "future_error" || failure.Error.Hint != "recover" {
				t.Fatalf("recovery failure changed: %+v %v", failure, err)
			}
			malformed = true
			if _, err := c.ReadFailure(t.Context(), "r1", "source", 3); err == nil {
				t.Fatal("duplicate inner error accepted")
			}
			got, err := c.ReconcileJob(t.Context(), "r1", "build", controllerwire.ReconcileRequest{Policy: "resume"})
			if err != nil || !reflect.DeepEqual(got, want) || calls != 5 {
				t.Fatalf("execution changed: %+v %v calls=%d", got, err, calls)
			}
		})
	}
}

func TestEngineAmendmentsNeverReplayOneTimeAuthority(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			here := controllerwire.CreateRunRequest{IdempotencyKey: "stable-intake"}
			here.Request.Options.Runner.Set, here.Request.Options.Runner.Value.Kind = true, runspec.RunnerHere
			for _, call := range []func(controllerwire.Controller) error{
				func(c controllerwire.Controller) error { _, err := c.CreateRun(t.Context(), here); return err },
				func(c controllerwire.Controller) error {
					_, err := c.Retry(t.Context(), "r1", controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: "stable-retry"})
					return err
				},
				func(c controllerwire.Controller) error {
					_, err := c.Claim(t.Context(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}})
					return err
				},
				func(c controllerwire.Controller) error {
					_, err := c.ReconcileJob(t.Context(), "r1", "build", controllerwire.ReconcileRequest{})
					return err
				},
				func(c controllerwire.Controller) error { _, err := c.Exchange(t.Context()); return err },
			} {
				calls := 0
				c, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(*http.Request) (*http.Response, error) {
					calls++
					resp := engineContractResponse(t, apierr.Envelope{Error: &apierr.Error{Code: apierr.Unavailable, Message: "response lost"}})
					resp.StatusCode = 503
					resp.Header = http.Header{"Retry-After": {"0"}}
					return resp, nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				var failure *apierr.Error
				if err := call(c); !errors.As(err, &failure) || failure.Code != apierr.Unavailable || calls != 1 {
					t.Fatalf("one-time authority replayed: calls=%d err=%v", calls, err)
				}
			}
		})
	}
}

func engineContractResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}
}
