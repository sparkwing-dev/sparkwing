package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func FuzzEngineStepReportKeepsRouteAndAttempt(f *testing.F) {
	f.Add("run", "generated/build", "--step=other", "running", int64(1))
	f.Add("run", "job", "verify", "failed", int64(4))
	f.Add("..", "job", "verify", "skipped", int64(3))
	f.Fuzz(func(t *testing.T, run, job, step, state string, ordinal int64) {
		input := controllerwire.StepReportRequest{StepID: step, State: state, SourceAttempt: ordinal}
		action := ""
		switch state {
		case "running":
			action = "start"
		case "passed", "failed", "cancelled":
			action = "finish"
		case "skipped":
			action = "skip"
		}
		inputErr := run == "" || run == "." || run == ".." || job == "" || job == "." || job == ".."
		for _, selector := range []string{run, job} {
			for _, char := range selector {
				inputErr = inputErr || char == '\r' || char == '\n' || char == 0
			}
		}
		valid := !inputErr && step != "" && ordinal > 0 && action != ""
		calls := 0
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			calls++
			path := "/api/v1/runs/" + url.PathEscape(run) + "/jobs/" + url.PathEscape(job) + "/steps/" + action
			if !valid || req.Method != "POST" || req.URL.EscapedPath() != path || req.URL.RawQuery != "" {
				t.Fatal("step input changed the declared route")
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			var got int64
			if err := json.Unmarshal(body["source_attempt"], &got); err != nil || got != ordinal || len(body) != 3 {
				t.Fatalf("step report changed attempt or included authority: %v", err)
			}
			return engineResponse(200, `{"recorded":true}`), nil
		})
		err := c.RecordStep(t.Context(), run, job, input)
		if valid && (err != nil || calls != 1) || !valid && (err == nil || calls != 0) {
			t.Fatalf("step report result: %v calls=%d", err, calls)
		}
	})
}

func TestEngineStepReportRequiresAcknowledgement(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"recorded":false}`, `{"recorded":null}`, `{"recorded":true,"recorded":false}`, `{"recorded":true,"error":{"code":"x","message":"failed"}}`} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if err := c.RecordStep(t.Context(), "run", "job", controllerwire.StepReportRequest{StepID: "verify", State: "passed", SourceAttempt: 2}); err == nil {
			t.Fatalf("unacknowledged persistence succeeded: %s", body)
		}
	}
}

func TestEngineStepReportReplaysExactBodyAndPreservesRefusals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var first string
		calls := 0
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			calls++
			var raw json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&raw); err != nil {
				t.Fatal(err)
			}
			if calls == 1 {
				first = string(raw)
				resp := engineResponse(503, `{"error":{"code":"unavailable","message":"retry"}}`)
				resp.Header.Set("Retry-After", "0")
				return resp, nil
			}
			if string(raw) != first {
				t.Fatal("step replay body changed")
			}
			return engineResponse(409, `{"error":{"code":"lock_held","message":"attempt ended"}}`), nil
		})
		err := c.RecordStep(t.Context(), "run", "job", controllerwire.StepReportRequest{StepID: "verify", State: "failed", SourceAttempt: 2, MaskedError: "masked"})
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.LockHeld || calls != 2 {
			t.Fatalf("step refusal changed: %v calls=%d", err, calls)
		}
	})
}
