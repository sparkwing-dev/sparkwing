package acceptance_test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestSurfaceStepReportTransport(t *testing.T) {
	started := time.Date(2026, 10, 15, 10, 0, 0, 123, time.UTC)
	finished := started.Add(time.Second)
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, state := range []string{"running", "passed", "failed", "cancelled", "skipped"} {
				input := controllerwire.StepReportRequest{StepID: "verify", State: state, SourceAttempt: 4, MaskedError: "masked diagnostic", StartedAt: &started}
				action := "finish"
				if state == "running" {
					action = "start"
				} else {
					input.FinishedAt = &finished
					if state == "skipped" {
						action = "skip"
					}
				}
				calls := 0
				controller, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != "POST" || req.URL.EscapedPath() != "/api/v1/runs/run/jobs/generated%2Fbuild/steps/"+action {
						t.Fatalf("unexpected step report: %s %s", req.Method, req.URL)
					}
					var got controllerwire.StepReportRequest
					if err := json.NewDecoder(req.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, input) {
						t.Fatalf("step report changed: %+v %v", got, err)
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"recorded":true}`))}, nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				if err := controller.RecordStep(t.Context(), "run", "generated/build", input); err != nil || calls != 1 {
					t.Fatalf("step report: %v calls=%d", err, calls)
				}
			}
		})
	}
}
