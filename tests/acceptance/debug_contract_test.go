package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestSurfaceDebugControlTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			calls := 0
			reader, err := client.NewDebugControl(client.EngineOptions{URL: endpoint, Bearer: func(context.Context) (string, error) { return "display-credential", nil }, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				path := "/api/v1/runs/412/jobs/build"
				body := `{"run_id":"run-412","number":412,"job":{"id":"build","status":"running","generation":2,"attempt":3,"state_seq":8,"expansion":"none","claim_bearer":"PRIVATE"},"execution":{"runner":"cloud-1","kind":"kubernetes","namespace":"ci","pod":"job-1","container":"runner","bootstrap":"PRIVATE"},"request":{"secret":"PRIVATE"}}`
				if calls == 2 {
					path = "/api/v1/runs/run-412/jobs/build/debug-pause"
					body = `{"run_id":"run-412","number":412,"job_id":"build","pause":null}`
				}
				if calls == 3 {
					raw, err := io.ReadAll(req.Body)
					if err != nil || string(raw) != "{}" || req.Method != "POST" || req.URL.Path != "/api/v1/runs/run-412/jobs/build/resume" || req.Header.Get("Authorization") != "Bearer display-credential" {
						t.Fatalf("debug release changed: %s %s %s %v", req.Method, req.URL, raw, err)
					}
					return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
				}
				if calls > 2 || req.Method != "GET" || req.URL.Path != path || req.Header.Get("Authorization") != "Bearer display-credential" {
					t.Fatalf("debug read changed: %s %s", req.Method, req.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			job, err := reader.ReadDebugJob(t.Context(), "412", "build")
			if err != nil || job.RunID != "run-412" || job.Job.Attempt != 3 || job.Execution == nil || job.Execution.Container != "runner" {
				t.Fatalf("debug job: %+v %v", job, err)
			}
			pause, err := reader.ReadDebugPause(t.Context(), job.RunID, job.Job.ID)
			if err != nil || pause.Pause != nil || calls != 2 {
				t.Fatalf("debug pause: %+v %v calls=%d", pause, err, calls)
			}
			raw, err := json.Marshal(job)
			if err != nil || strings.Contains(string(raw), "PRIVATE") {
				t.Fatal("private fields entered the public projection")
			}
			if err := reader.ResumeDebugPause(t.Context(), job.RunID, job.Job.ID); err != nil || calls != 3 {
				t.Fatalf("resume: calls=%d err=%v", calls, err)
			}
		})
	}
}
