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
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSurfaceGrammarContract(t *testing.T) {
	for _, command := range []string{
		"pipeline list", "runs list", "schedules list", "runners list", "pools list",
		"tokens list", "secrets list", "profiles list", "queue list", "daemon status",
		"daemon events --run r1 --limit 1", "daemon events --kind queued,grant --kind release --since 1d",
		"daemon events --run r1 --explain",
		"dashboard status", "cache info", "repos list", "debug env --run r1 --job j1",
		"pipeline sparks inflate --library templates",
		"cache warm", "wingd run --stay --home private-home", "__tick --home private-home", "operator limits show", "doctor", "info", "version", "update", "docs list", "examples", "commands", "completion",
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
		"update --cli", "update --check", "update --sdk", "pipeline sparks warmup",
		"pipeline sparks inflate --module templates", "pipeline sparks inflate",
		"runs list --limit 0", "runs list --node build", "runs status r1", "run check --unknown", "run check --profile prod",
		"daemon events --limit 0", "daemon events --offset 1", "daemon events --incarnation -1",
		"daemon events --explain", "daemon events --run r1 --explain --cursor opaque", "pipeline hooks status --prove",
		"daemon events --run r1 --explain --limit 1",
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
			status, body := http.StatusOK, `{"runs":[],"next_cursor":"","truncated":false}`
			switch req.Method {
			case http.MethodGet:
				q := req.URL.Query()
				if req.URL.Path != "/api/v1/runs" || q.Get("after_number") != "412" || q.Get("root_only") != "true" || q.Get("finished_before") != "2026-10-14T00:00:00Z" || !reflect.DeepEqual(q["branch"], []string{"main", "!release"}) {
					t.Fatalf("query lost: %s", req.URL)
				}
			case http.MethodDelete:
				if req.URL.Path != "/api/v1/runs/412" {
					t.Fatalf("delete selector: %s", req.URL)
				}
				status, body = http.StatusNoContent, ""
			default:
				t.Fatalf("unexpected method: %s", req.Method)
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 20, AfterNumber: 412, RootOnly: true, Branches: []string{"main", "!release"}, FinishedBefore: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteRun(t.Context(), "412"); err != nil || calls != 2 {
			t.Fatalf("deletion: calls=%d err=%v", calls, err)
		}
	}
}

func TestSurfaceManagementTransportsKeepAuthorityNamespaces(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		var calls []string
		options := client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
			calls = append(calls, req.Method+" "+req.URL.Path)
			body := ""
			switch req.URL.Path {
			case "/api/v1/pools":
				body = `{"pools":[{"name":"prod","runners":["desktop"],"rules":[{"id":"rule-1","repos":["acme/api"],"branches":["main"],"source":"github","route":true}]}]}`
			case "/api/v1/tokens":
				body = `{"tokens":[{"prefix":"swk_member","team":"team","audience":"controller","created_by":"owner","git_credentials":false,"metered":false,"subject":{"kind":"account","id":"build"},"grants":["runs.read"],"issued_at":"2026-01-01T00:00:00Z"}]}`
			case "/api/v1/operator/tokens":
				body = `{"tokens":[{"prefix":"swk_deployment","team":"","audience":"controller","created_by":"owner","git_credentials":false,"metered":false,"subject":{"kind":"service","id":"deploy"},"grants":["operator.tokens"],"issued_at":"2026-01-01T00:00:00Z"}]}`
			case "/api/v1/sizing":
				encoded, err := json.Marshal(controllerwire.SizingPage{Sizing: []controllerwire.SizingDetail{{Pipeline: "check", Repo: "acme/api", Source: "measured", MemoExcluded: 5, Rollup: controllerwire.SizingMeasurements{PeakCores: 6.5, PeakMemoryBytes: 7 << 30}, Jobs: []controllerwire.JobSizing{}}}})
				if err != nil {
					t.Fatal(err)
				}
				body = string(encoded)
			default:
				t.Fatalf("unexpected management request: %s", req.URL)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}}
		pools, err := client.NewPoolManager(options)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := pools.ListPools(t.Context())
		if err != nil || len(listed.Pools) != 1 || len(listed.Pools[0].Rules) != 1 || !listed.Pools[0].Rules[0].Route {
			t.Fatalf("pools: %+v %v", listed, err)
		}
		for _, deployment := range []bool{false, true} {
			tokens, err := client.NewTokenManager(options, deployment)
			if err != nil {
				t.Fatal(err)
			}
			listed, err := tokens.ListTokens(t.Context(), controllerwire.TokenQuery{})
			if err != nil || len(listed.Tokens) != 1 {
				t.Fatalf("tokens: %+v %v", listed, err)
			}
			kind := "member"
			if deployment {
				kind = "service"
			}
			if listed.Tokens[0].Kind != kind {
				t.Fatalf("credential namespace crossed: %+v", listed)
			}
		}
		sizing, err := client.NewSizingManager(options)
		if err != nil {
			t.Fatal(err)
		}
		measurements, err := sizing.ListSizing(t.Context(), controllerwire.SizingQuery{Limit: 20})
		if err != nil || len(measurements.Sizing) != 1 || measurements.Sizing[0].Rollup.PeakCores != 6.5 || measurements.Sizing[0].MemoExcluded != 5 {
			t.Fatalf("sizing: %+v %v", measurements, err)
		}
		want := []string{"GET /api/v1/pools", "GET /api/v1/tokens", "GET /api/v1/operator/tokens", "GET /api/v1/sizing"}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("routes: %v", calls)
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
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
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
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
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
