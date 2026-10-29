package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestTriggeredOutcomeUsesOnePublicRunDetail(t *testing.T) {
	for _, status := range []string{"success", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			reader := &statusReader{details: []controllerwire.RunDetail{{ID: "run-outcome", Status: status, Jobs: []controllerwire.JobDetail{{ID: "build", Status: "finished", Outcome: "failed", Attempt: 2, StatusDetail: "undefined: Frobnicate"}}}}}
			var out bytes.Buffer
			got, err := triggeredRunOutcome(t.Context(), reader, "run-outcome", &out)
			if err != nil || got != status || len(reader.selectors) != 1 {
				t.Fatalf("status=%q reads=%v error=%v", got, reader.selectors, err)
			}
			if status == "success" {
				if out.Len() != 0 {
					t.Fatalf("success wrote a failure summary: %q", &out)
				}
				return
			}
			for _, want := range []string{"run-outcome", "status:    " + status, "build", "undefined: Frobnicate"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("summary missing %q: %q", want, &out)
				}
			}
		})
	}
}

func TestTriggeredOutcomeReadAndWriteFailuresStayLoud(t *testing.T) {
	refusal := errors.New("status unavailable")
	reader := &statusReader{err: refusal}
	var out bytes.Buffer
	if status, err := triggeredRunOutcome(t.Context(), reader, "run-outcome", &out); status != "" || !errors.Is(err, refusal) || out.Len() != 0 {
		t.Fatalf("status=%q error=%v output=%q", status, err, &out)
	}
	reader = &statusReader{details: []controllerwire.RunDetail{{ID: "other", Status: "success"}}}
	if status, err := triggeredRunOutcome(t.Context(), reader, "run-outcome", &out); status != "" || err == nil {
		t.Fatalf("different run accepted: status=%q error=%v", status, err)
	}
	reader = &statusReader{details: []controllerwire.RunDetail{{ID: "run-outcome", Status: "failed"}}}
	if _, err := triggeredRunOutcome(t.Context(), reader, "run-outcome", retryBrokenWriter{}); err == nil {
		t.Fatal("lost failure summary reported success")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := triggeredRunOutcome(ctx, reader, "run-outcome", &out); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}

func TestTriggeredOutcomeRetainsVersionPolicyNotices(t *testing.T) {
	reader := &statusReader{details: []controllerwire.RunDetail{{ID: "run-outcome", Status: "success", Notices: []engineid.Notice{{Reason: "release will be revoked", Hint: "sparkwing upgrade"}}}}}
	var out bytes.Buffer
	if _, err := triggeredRunOutcome(t.Context(), reader, "run-outcome", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "release will be revoked") || !strings.Contains(out.String(), "sparkwing upgrade") {
		t.Fatalf("notices lost: %q", &out)
	}
}

func TestTriggeredOutcomeUsesSharedReadRetryPolicy(t *testing.T) {
	for _, temporary := range []bool{false, true} {
		calls := 0
		reader, err := client.NewRunReader(client.EngineOptions{URL: "http://api.sock", HTTP: &http.Client{Transport: profileTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.Method != http.MethodGet || req.URL.Path != "/api/v1/runs/run-outcome" {
				t.Fatalf("unexpected outcome read: %s %s", req.Method, req.URL)
			}
			if calls == 1 {
				code, status := "forbidden", http.StatusForbidden
				if temporary {
					code, status = "unavailable", http.StatusServiceUnavailable
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"0"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"` + code + `","message":"refused"}}`))}, nil
			}
			body, err := json.Marshal(controllerwire.RunDetail{ID: "run-outcome", Status: "failed", Jobs: []controllerwire.JobDetail{{ID: "build", Outcome: "failed", StatusDetail: "compiler failure"}}})
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		status, err := triggeredRunOutcome(t.Context(), reader, "run-outcome", &out)
		if temporary {
			if err != nil || status != "failed" || calls != 2 || !strings.Contains(out.String(), "compiler failure") {
				t.Fatalf("retry: status=%s calls=%d output=%q err=%v", status, calls, &out, err)
			}
		} else if err == nil || calls != 1 || out.Len() != 0 {
			t.Fatalf("refusal retried: calls=%d output=%q err=%v", calls, &out, err)
		}
	}
}
