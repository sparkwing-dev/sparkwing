package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

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
