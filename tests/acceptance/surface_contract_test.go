package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestSurfaceGrammarContract(t *testing.T) {
	for _, command := range []string{
		"pipeline list", "runs list", "schedules list", "runners list", "pools list",
		"tokens list", "secrets list", "profiles list", "queue list", "daemon status",
		"dashboard status", "cache info", "repos list", "debug env --run r1 --job j1",
		"operator limits show", "doctor", "info", "version", "update", "docs list", "examples", "commands", "completion",
	} {
		for _, format := range []string{"pretty", "json", "plain"} {
			args := append(strings.Fields(command), "-o", format)
			inv, err := clisurface.Parse("sparkwing", args)
			if err != nil || inv.Output(false) != format {
				t.Fatalf("grammar %v: %v", args, err)
			}
		}
	}
	for _, command := range []string{
		"cluster", "cloud", "configure", "serve", "crons", "runs bounce", "runs consumer", "debug rerun",
		"runs list --limit 0", "runs list --node build", "runs status r1", "run check --unknown", "run check --profile prod",
	} {
		if _, err := clisurface.Parse("sparkwing", strings.Fields(command)); err == nil {
			t.Fatalf("removed or malformed invocation accepted: %s", command)
		}
	}
	for _, command := range []string{"--profile prod pipeline trigger check", "pipeline trigger check --profile prod"} {
		inv, err := clisurface.Parse("sparkwing", append(strings.Fields(command), "--", "--profile", "argument"))
		if err != nil || inv.Value("profile") != "prod" || inv.Pipeline != "check" || !reflect.DeepEqual(inv.Args, []string{"--profile", "argument"}) {
			t.Fatalf("trigger profile position: %#v %v", inv, err)
		}
	}
}

func TestSurfaceRunQueryAndPruneTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		calls := 0
		c, err := client.NewRunPruner(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			body := `{"runs":[],"next_cursor":"","truncated":false}`
			switch req.Method {
			case "GET":
				q := req.URL.Query()
				if req.URL.Path != "/api/v1/runs" || q.Get("after_number") != "412" || q.Get("root_only") != "true" || q.Get("finished_before") != "2026-10-14T00:00:00Z" || !reflect.DeepEqual(q["branch"], []string{"main", "!release"}) {
					t.Fatalf("query lost: %s", req.URL)
				}
			case "DELETE":
				if req.URL.Path != "/api/v1/runs/412" {
					t.Fatalf("delete selector: %s", req.URL)
				}
				body = `{"run_id":"r1","deleted":true}`
			default:
				t.Fatalf("unexpected method: %s", req.Method)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 20, AfterNumber: 412, RootOnly: true, Branches: []string{"main", "!release"}, FinishedBefore: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		deleted, err := c.DeleteRun(t.Context(), "412")
		if err != nil || deleted.RunID != "r1" || !deleted.Deleted || calls != 2 {
			t.Fatalf("deletion: %+v calls=%d err=%v", deleted, calls, err)
		}
	}
}

type surfaceTransport func(*http.Request) (*http.Response, error)

func (f surfaceTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSurfaceWireDetailTransport(t *testing.T) {
	// safety: W2 owns the server adapter; both transports deliver the same authoritative fixture.
	want := controllerwire.RunDetail{
		ID: "r1", Number: 412, Team: "acme", Status: "success", StateSeq: 8,
		Jobs:     []controllerwire.JobDetail{{ID: "build", Status: "finished", Outcome: "memoized", Attempt: 2, Generation: 3, StateSeq: 7, Expansion: "succeeded"}},
		Attempts: []controllerwire.AttemptDetail{{JobID: "build", Attempt: 2, Generation: 3, Outcome: "memoized", Executor: "runner-1"}},
	}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		c, err := client.NewEngineClient(client.EngineOptions{
			URL:    endpoint,
			Bearer: func(context.Context) (string, error) { return "bound-capability", nil },
			HTTP: &http.Client{Transport: surfaceTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/api/v1/runs/r1" || r.Header.Get("Authorization") != "Bearer bound-capability" {
					t.Fatalf("wrong detail request: %s", r.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.ReadRun(t.Context(), "r1")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s lost detail: %#v, %v", endpoint, got, err)
		}
	}
}

func TestSurfaceRunObservationTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		calls := []string{}
		reader, err := client.NewRunReader(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
			calls = append(calls, req.URL.RequestURI())
			body := ""
			switch req.URL.Path {
			case "/api/v1/runs":
				body = `{"runs":[],"next_cursor":"","truncated":false}`
			case "/api/v1/runs/412/steps":
				body = `{"steps":[{"job_id":"build","id":"compile","label":"Compile","attempt":2,"status":"finished","outcome":"success"}]}`
			case "/api/v1/runs/412/receipt":
				body = `{"run_id":"r1","pipeline":"check","git_sha":"abc","status":"success","duration_ms":1,"identity":{"pipeline_version_hash":"version","inputs_hash":"public","plan_hash":"plan","outputs_hash":{}},"steps":[],"cost":{"currency":"USD","compute_cents":5,"rate_source":"ledger","settled":true},"receipt_sha":"authoritative"}`
			default:
				t.Fatalf("unexpected observation %s", req.URL)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 20, ParentRunID: "412"}); err != nil {
			t.Fatal(err)
		}
		steps, err := reader.ReadSteps(t.Context(), "412")
		if err != nil || len(steps.Steps) != 1 || steps.Steps[0].JobID != "build" || steps.Steps[0].Attempt != 2 {
			t.Fatalf("step projection: %+v %v", steps, err)
		}
		receipt, err := reader.ReadReceipt(t.Context(), "412")
		if err != nil || receipt.ReceiptSHA != "authoritative" || receipt.Cost.ComputeCents != 5 || !receipt.Cost.Settled {
			t.Fatalf("receipt projection: %+v %v", receipt, err)
		}
		if !reflect.DeepEqual(calls, []string{"/api/v1/runs?limit=20&parent_run_id=412", "/api/v1/runs/412/steps", "/api/v1/runs/412/receipt"}) {
			t.Fatalf("observation routes: %v", calls)
		}
	}
}
