package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRunReviewRoutesAndAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, response string
		call                               func(controllerwire.RunReview) (any, error)
	}{
		{"pending", "GET", "/api/v1/approvals?pending=true", "", `{"approvals":[]}`, func(c controllerwire.RunReview) (any, error) { return c.ListApprovals(t.Context(), "") }},
		{"history", "GET", "/api/v1/runs/412/approvals", "", `{"approvals":[{"run_id":"r1","job_id":"ship","requested_at":"2026-10-14T10:00:00Z","message":"ship?","timeout_ms":0,"on_timeout":"fail"}]}`, func(c controllerwire.RunReview) (any, error) { return c.ListApprovals(t.Context(), "412") }},
		{"resolve", "POST", "/api/v1/runs/412/approvals/deploy%2Fprod", `{"resolution":"approved","comment":"safe"}`, `{"approval":{"run_id":"r1","job_id":"deploy/prod","requested_at":"2026-10-14T10:00:00Z","resolved_at":"2026-10-14T10:01:00Z","message":"ship?","timeout_ms":0,"on_timeout":"fail","resolution":"approved","approver":"authenticated-user","comment":"safe"},"replayed":true}`, func(c controllerwire.RunReview) (any, error) {
			return c.ResolveApproval(t.Context(), "412", "deploy/prod", controllerwire.ApprovalDecision{Resolution: "approved", Comment: "safe", IdempotencyKey: "stable-key"})
		}},
		{"annotate", "POST", "/api/v1/runs/412/jobs/build/annotations", `{"level":"info","message":"note"}`, ``, func(c controllerwire.RunReview) (any, error) {
			return nil, c.AppendAnnotation(t.Context(), "412", "build", controllerwire.AnnotationWrite{Annotation: controllerwire.Annotation{Level: "info", Message: "note"}})
		}},
		{"step", "POST", "/api/v1/runs/412/jobs/build/steps/annotations", `{"level":"warn","message":"note","file":"source.go","line":7,"column":3,"end_line":8,"step_id":"compile"}`, ``, func(c controllerwire.RunReview) (any, error) {
			return nil, c.AppendAnnotation(t.Context(), "412", "build", controllerwire.AnnotationWrite{StepID: "compile", Annotation: controllerwire.Annotation{Level: "warn", Message: "note", File: "source.go", Line: 7, Column: 3, EndLine: 8}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewRunReview(EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "credential", nil }, HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				if req.Method != tc.method || req.URL.RequestURI() != tc.path || string(body) != tc.body || req.Header.Get("Authorization") != "Bearer credential" {
					t.Fatalf("request %s %s %s", req.Method, req.URL.RequestURI(), body)
				}
				if tc.name == "resolve" && req.Header.Get("Idempotency-Key") != "stable-key" {
					t.Fatal("missing decision idempotency key")
				}
				if strings.Contains(string(body), "approver") {
					t.Fatal("client selected approver")
				}
				status := http.StatusOK
				if tc.response == "" {
					status = http.StatusNoContent
				}
				return engineResponse(status, tc.response), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := tc.call(c)
			if err != nil {
				t.Fatal(err)
			}
			if result, ok := got.(controllerwire.ApprovalResult); ok && (!result.Replayed || result.Approval.Approver != "authenticated-user") {
				t.Fatalf("lost controller result: %+v", result)
			}
		})
	}
}

func TestRunReviewValidatesBeforeTransport(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid request reached HTTP"); return nil, nil })
	for _, input := range []controllerwire.ApprovalDecision{{Resolution: "timed_out", IdempotencyKey: "k"}, {Resolution: "approved"}} {
		if _, err := c.ResolveApproval(t.Context(), "r", "j", input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
	for _, input := range []controllerwire.AnnotationWrite{{Annotation: controllerwire.Annotation{Level: "", Message: "x"}}, {StepID: "..", Annotation: controllerwire.Annotation{Level: "info", Message: "x"}}} {
		if err := c.AppendAnnotation(t.Context(), "r", "j", input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
	if _, err := c.ListApprovals(t.Context(), ".."); err == nil {
		t.Fatal("invalid run accepted")
	}
}

func TestRunReviewPreservesForbiddenAndRejectsUnresolvedSuccess(t *testing.T) {
	failure := testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(403, `{"error":{"code":"forbidden","message":"reporting credential required","hint":"use an authorized credential"}}`), nil
	})
	err := failure.AppendAnnotation(t.Context(), "r", "j", controllerwire.AnnotationWrite{Annotation: controllerwire.Annotation{Level: "info", Message: "x"}})
	var api *apierr.Error
	if !errors.As(err, &api) || api.Code != apierr.Forbidden || api.Hint != "use an authorized credential" {
		t.Fatalf("lost authorization failure: %v", err)
	}
	malformed := testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(200, `{"approval":{"run_id":"r","job_id":"j","requested_at":"2026-10-14T10:00:00Z","message":"gate","timeout_ms":0,"on_timeout":"fail"},"replayed":false}`), nil
	})
	if _, err := malformed.ResolveApproval(t.Context(), "r", "j", controllerwire.ApprovalDecision{Resolution: "approved", IdempotencyKey: "k"}); err == nil {
		t.Fatal("unresolved approval reported success")
	}
}

func TestRunReviewDecisionRetryKeepsExactBodyAndKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != `{"resolution":"denied","comment":"reviewed"}` || req.Header.Get("Idempotency-Key") != "decision-key" {
				t.Fatalf("replay changed: %s %q", body, req.Header.Get("Idempotency-Key"))
			}
			if calls == 1 {
				resp := engineResponse(503, `{"error":{"code":"unavailable","message":"retry"}}`)
				resp.Header.Set("Retry-After", "1")
				return resp, nil
			}
			return engineResponse(200, `{"approval":{"run_id":"r","job_id":"j","requested_at":"2026-10-14T10:00:00Z","resolved_at":"2026-10-14T10:01:00Z","message":"gate","timeout_ms":0,"on_timeout":"fail","resolution":"denied","approver":"user"},"replayed":true}`), nil
		})
		result, err := c.ResolveApproval(t.Context(), "r", "j", controllerwire.ApprovalDecision{Resolution: "denied", Comment: "reviewed", IdempotencyKey: "decision-key"})
		if err != nil || calls != 2 || !result.Replayed {
			t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
		}
	})
}

func TestRunReviewAnnotationObservationRoundTrip(t *testing.T) {
	annotation := controllerwire.Annotation{Level: "warn", Message: "masked note", File: "source.go", Line: 7, Column: 3, EndLine: 8}
	run := controllerwire.RunDetail{ID: "r", Jobs: []controllerwire.JobDetail{{ID: "build", Annotations: []controllerwire.Annotation{annotation}}}}
	body, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, string(body)), nil })
	got, err := c.ReadRun(t.Context(), "412")
	if err != nil || len(got.Jobs) != 1 || len(got.Jobs[0].Annotations) != 1 || got.Jobs[0].Annotations[0] != annotation {
		t.Fatalf("annotation roundtrip: %+v %v", got, err)
	}
	steps := controllerwire.RunSteps{Steps: []controllerwire.StepDetail{{JobID: "build", ID: "compile", Annotations: []controllerwire.Annotation{annotation}}}}
	body, err = json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	stepResult, err := c.ReadSteps(t.Context(), "412")
	if err != nil || len(stepResult.Steps) != 1 || len(stepResult.Steps[0].Annotations) != 1 || stepResult.Steps[0].Annotations[0] != annotation {
		t.Fatalf("step roundtrip: %+v %v", stepResult, err)
	}
}
