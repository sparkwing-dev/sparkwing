package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func debugJobFixture() controllerwire.DebugJob {
	return controllerwire.DebugJob{RunID: "run-1", Number: 412, Job: controllerwire.JobDetail{ID: "build", Status: "running", Generation: 2, Attempt: 3, StateSeq: 8, Expansion: "none"}, Execution: &controllerwire.DebugExecution{Kind: "kubernetes", Runner: "cloud-1", Namespace: "ci", Pod: "run-1-job-1", Container: "runner"}}
}

func debugJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDebugReadsUseFinalRoutes(t *testing.T) {
	job := debugJobFixture()
	pause := controllerwire.DebugPauseResult{RunID: job.RunID, Number: job.Number, JobID: job.Job.ID, Pause: &controllerwire.DebugPause{Reason: "before", PausedAt: time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)}}
	calls := 0
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		calls++
		path, value := "/api/v1/runs/412/jobs/build", any(job)
		if calls > 1 {
			path, value = "/api/v1/runs/run-1/jobs/build/debug-pause", pause
		}
		if req.Method != http.MethodGet || req.URL.Path != path || req.URL.RawQuery != "" || req.Header.Get("Authorization") != "Bearer private-capability" {
			t.Fatalf("debug request: %s %s %v", req.Method, req.URL, req.Header)
		}
		return engineResponse(200, debugJSON(t, value)), nil
	})
	result, err := c.ReadDebugJob(t.Context(), "412", "build")
	if err != nil || result.Execution == nil || result.Execution.Pod != job.Execution.Pod || result.RunID != job.RunID {
		t.Fatalf("job: %+v %v", result, err)
	}
	observed, err := c.ReadDebugPause(t.Context(), result.RunID, "build")
	if err != nil || observed.Pause == nil || observed.Pause.Reason != "before" || observed.Pause.ExpiresAt != nil || calls != 2 {
		t.Fatalf("pause: %+v %v calls=%d", observed, err, calls)
	}
}

func TestDebugReadsRejectMalformedAndMismatchedProjections(t *testing.T) {
	job := debugJobFixture()
	for _, mutate := range []func(*controllerwire.DebugJob){
		func(v *controllerwire.DebugJob) { v.RunID = "other" },
		func(v *controllerwire.DebugJob) { v.Job.ID = "other" },
		func(v *controllerwire.DebugJob) { v.Job.Status = "" },
		func(v *controllerwire.DebugJob) { v.Execution.Kind = "pod:guess" },
		func(v *controllerwire.DebugJob) { v.Execution.Pod = "" },
		func(v *controllerwire.DebugJob) { v.Execution.Namespace = "" },
	} {
		value := debugJobFixture()
		mutate(&value)
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, debugJSON(t, value)), nil })
		if _, err := c.ReadDebugJob(t.Context(), job.RunID, job.Job.ID); err == nil {
			t.Fatalf("accepted malformed job: %+v", value)
		}
	}
	for _, raw := range []string{"null", "{}", `{"run_id":"run-1","number":412,"job_id":"other","pause":null}`, `{"run_id":"run-1","number":412,"job_id":"build","pause":{}}`, `{"run_id":"run-1","number":412,"job_id":"build","pause":{"reason":"before","paused_at":"0001-01-01T00:00:00Z","expires_at":null}}`, `{"run_id":"run-1","number":412,"job_id":"build","pause":{"reason":"before","paused_at":"2026-10-15T01:00:00Z","expires_at":"2026-10-15T00:00:00Z"}}`} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, raw), nil })
		if _, err := c.ReadDebugPause(t.Context(), "run-1", "build"); err == nil {
			t.Fatalf("accepted malformed pause: %s", raw)
		}
	}
}

func TestDebugAbsenceAndReadFailuresStayDistinct(t *testing.T) {
	for _, status := range []int{200, 403, 404, 409} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) {
			if status == 200 {
				return engineResponse(status, `{"run_id":"run-1","number":412,"job_id":"build","pause":null}`), nil
			}
			return engineResponse(status, `{"error":{"code":"future_denial","message":"refused"}}`), nil
		})
		value, err := c.ReadDebugPause(t.Context(), "run-1", "build")
		if status == 200 {
			if err != nil || value.Pause != nil {
				t.Fatalf("absent pause: %+v %v", value, err)
			}
		} else {
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != "future_denial" {
				t.Fatalf("read failure lost: %v", err)
			}
		}
	}
}

func FuzzDebugReadsDropPrivateAdditiveFields(f *testing.F) {
	f.Add("bearer-never-public", "--pod=other")
	f.Fuzz(func(t *testing.T, secret, selector string) {
		if secret == "" || len(secret) > 4096 || len(selector) > 4096 {
			t.Skip()
		}
		job := debugJobFixture()
		var data map[string]any
		if err := json.Unmarshal([]byte(debugJSON(t, job)), &data); err != nil {
			t.Fatal(err)
		}
		data["bearer"], data["request"], data["environment"] = secret, map[string]string{"secret": secret}, map[string]string{"SECRET": secret}
		data["job"].(map[string]any)["claim_bearer"] = secret
		data["execution"].(map[string]any)["bootstrap"] = secret
		calls := 0
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			calls++
			want := job.Job.ID
			if calls > 1 {
				want = selector
			}
			if req.Method != http.MethodGet || req.URL.EscapedPath() != "/api/v1/runs/run-1/jobs/"+url.PathEscape(want) || req.URL.RawQuery != "" {
				t.Fatal("selector changed the declared route")
			}
			return engineResponse(200, debugJSON(t, data)), nil
		})
		read, err := c.ReadDebugJob(t.Context(), job.RunID, job.Job.ID)
		if err != nil || debugJSON(t, read) != debugJSON(t, job) {
			t.Fatalf("private fields changed the public projection: %v", err)
		}
		_, err = c.ReadDebugJob(t.Context(), job.RunID, selector)
		if !validSelector(selector) && (err == nil || calls != 1) {
			t.Fatal("invalid selector reached transport")
		}
	})
}

func TestDebugResumeRequiresControllerAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		ok     bool
	}{
		{204, "", true},
		{200, `{}`, false},
		{202, `{}`, false},
		{204, `{}`, false},
		{403, `{"error":{"code":"forbidden","message":"scope missing"}}`, false},
	} {
		calls := 0
		c := testEngine(t, func(r *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(r.Body)
			if err != nil || r.Method != http.MethodPost || r.URL.Path != "/api/v1/runs/run-1/jobs/build/resume" || r.Header.Get("Authorization") != "Bearer private-capability" || string(body) != "{}" {
				t.Fatalf("release request: %s %s %s %v", r.Method, r.URL, body, err)
			}
			return engineResponse(tc.status, tc.body), nil
		})
		err := c.ResumeDebugPause(t.Context(), "run-1", "build")
		if (err == nil) != tc.ok || calls != 1 {
			t.Fatalf("HTTP %d body %s: calls=%d err=%v", tc.status, tc.body, calls, err)
		}
	}
}

func TestDebugResumeReplaysOnlyTheSelectedEmptyRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		c := testEngine(t, func(r *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(r.Body)
			if err != nil || r.URL.Path != "/api/v1/runs/run-1/jobs/build/resume" || string(body) != "{}" {
				t.Fatalf("changed release: %s %s %v", r.URL, body, err)
			}
			if calls == 1 {
				resp := engineResponse(503, `{"error":{"code":"unavailable","message":"retry"}}`)
				resp.Header.Set("Retry-After", "1")
				return resp, nil
			}
			return engineResponse(204, ""), nil
		})
		if err := c.ResumeDebugPause(t.Context(), "run-1", "build"); err != nil || calls != 2 {
			t.Fatalf("replay: calls=%d err=%v", calls, err)
		}
	})
}

func FuzzDebugResumeSelectorsStayInTheMatchedRoute(f *testing.F) {
	f.Add("run-1", "build")
	f.Add("run/other?token=value", "../resume#fragment")
	f.Add("", "build")
	f.Fuzz(func(t *testing.T, run, job string) {
		calls := 0
		c := testEngine(t, func(r *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != "{}" || r.URL.EscapedPath() != "/api/v1/runs/"+url.PathEscape(run)+"/jobs/"+url.PathEscape(job)+"/resume" || r.URL.RawQuery != "" || r.URL.Fragment != "" {
				t.Fatalf("selectors escaped the operation: %s %s %v", r.URL, body, err)
			}
			return engineResponse(204, ""), nil
		})
		err := c.ResumeDebugPause(t.Context(), run, job)
		if !validSelector(run) || !validSelector(job) {
			if err == nil || calls != 0 {
				t.Fatal("invalid selector reached release transport")
			}
		} else if err != nil || calls != 1 {
			t.Fatalf("valid selector: calls=%d err=%v", calls, err)
		}
	})
}
