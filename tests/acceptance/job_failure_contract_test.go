package acceptance_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestSurfaceJobFailureReportTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			var report controllerwire.JobTransitionRequest
			body := `{"to":"failed","cause":"report","result":{"outcome":"failed","reason":"step_failed","failure":{"job":"build","stage":"step:verify","error":{"code":"future_failure","message":"masked failure","hint":"retry verification","details":{"exit_code":7}}}}}`
			if err := json.Unmarshal([]byte(body), &report); err != nil {
				t.Fatal(err)
			}
			calls := 0
			controller, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "POST" || req.URL.Path != "/api/v1/runs/run/jobs/build/transition" {
					t.Fatalf("unexpected report: %s %s", req.Method, req.URL)
				}
				var got struct {
					Result struct {
						Failure struct {
							Job, Stage string
							Error      struct {
								Code, Message, Hint string
								Details             map[string]int
							}
						}
						Error json.RawMessage
					}
				}
				if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				failure := got.Result.Failure
				if failure.Job != "build" || failure.Stage != "step:verify" || failure.Error.Code != "future_failure" || failure.Error.Message != "masked failure" || failure.Error.Hint != "retry verification" || failure.Error.Details["exit_code"] != 7 || got.Result.Error != nil {
					t.Fatalf("report lost its structured failure: %+v", got)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"replayed":false,"cancel_requested":false,"state_seq":3,"generation":1,"attempt":1}`))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			applied, err := controller.TransitionJob(t.Context(), "run", "build", report)
			if err != nil || calls != 1 || applied.Attempt != 1 || applied.StateSeq != 3 {
				t.Fatalf("report: %+v, calls=%d, %v", applied, calls, err)
			}
		})
	}
}
