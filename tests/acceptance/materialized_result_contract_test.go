package acceptance_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSurfaceMaterializedResultTransport(t *testing.T) {
	identity := controllerwire.MaterializedResultSource{RunID: "memo-run", JobID: "generated/build", StateSeq: 9007199254740993}
	response, err := json.Marshal(controllerwire.ReconcileResponse{
		Kind: "materialize", Job: controllerwire.ExecutionJob{JobDetail: controllerwire.JobDetail{ID: "build", Status: "done", Outcome: "memoized"}},
		Source: &controllerwire.ReuseSource{Policy: "manual_retry", Result: &identity},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			calls := 0
			controller, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "POST" || req.URL.Path != "/api/v1/runs/retry/jobs/build/reconcile" {
					t.Fatalf("unexpected reconciliation: %s %s", req.Method, req.URL)
				}
				var body struct {
					Policy string `json:"policy"`
					Source struct {
						Policy string `json:"policy"`
						Result struct {
							Run   string `json:"run_id"`
							Job   string `json:"job_id"`
							State int64  `json:"state_seq"`
						} `json:"result"`
					} `json:"source"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Policy != "manual_retry" || body.Source.Policy != "manual_retry" || body.Source.Result.Run != "memo-run" || body.Source.Result.Job != "generated/build" || body.Source.Result.State != 9007199254740993 {
					t.Fatalf("materialized identity changed: %+v", body)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(response)))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := controller.ReconcileJob(t.Context(), "retry", "build", controllerwire.ReconcileRequest{Policy: "manual_retry", Source: &controllerwire.ReuseSource{Policy: "manual_retry", Result: &identity}})
			if err != nil || calls != 1 || result.Kind != "materialize" || result.Job.Attempt != 0 || result.Source == nil || result.Source.Result == nil || *result.Source.Result != identity {
				t.Fatalf("materialized result changed: %+v calls=%d err=%v", result, calls, err)
			}
		})
	}
}

func TestSurfaceSnapshotPreservesGuardRefusal(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		for _, cancelled := range []bool{false, true} {
			body, err := json.Marshal(map[string]any{
				"run_id": "run", "store_now": "2026-10-15T00:00:00Z", "cancel_requested": cancelled,
				"guard_refusal": "branch guard refused", "jobs": []any{}, "generators": []any{}, "expansions": []any{},
			})
			if err != nil {
				t.Fatal(err)
			}
			controller, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Path != "/api/v1/runs/run/snapshot" {
					t.Fatalf("unexpected snapshot: %s %s", req.Method, req.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := controller.ReadSnapshot(t.Context(), "run")
			if err != nil || got.GuardRefusal != "branch guard refused" || got.CancelRequested != cancelled {
				t.Fatalf("sealed decision changed: %+v, %v", got, err)
			}
		}
	}
}
