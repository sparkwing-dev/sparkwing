package client

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRunReaderRoutesAndPublicShapes(t *testing.T) {
	for _, tc := range []struct {
		path string
		body string
		call func(controllerwire.RunReader) (any, error)
	}{
		{"/api/v1/runs?cursor=a%26b&limit=7&parent_run_id=412", `{"runs":[],"next_cursor":"next","truncated":true,"future":42}`, func(c controllerwire.RunReader) (any, error) {
			return c.ListRuns(t.Context(), controllerwire.RunQuery{ParentRunID: "412", Limit: 7, Cursor: "a&b"})
		}},
		{"/api/v1/runs/412/steps", `{"steps":[{"job_id":"build","id":"compile","label":"Build","attempt":2,"status":"finished","outcome":"failed","reason":"masked"}]}`, func(c controllerwire.RunReader) (any, error) { return c.ReadSteps(t.Context(), "412") }},
		{"/api/v1/runs/412/receipt", `{"run_id":"r1","pipeline":"check","git_sha":"abc","status":"success","duration_ms":15,"identity":{"pipeline_version_hash":"v1","inputs_hash":"safe","plan_hash":"plan","outputs_hash":{}},"steps":[{"id":"compile","job_id":"build","duration_ms":15,"outcome":"success"}],"cost":{"currency":"USD","compute_cents":27,"rate_source":"settled ledger","settled":true},"receipt_sha":"sha256:original"}`, func(c controllerwire.RunReader) (any, error) { return c.ReadReceipt(t.Context(), "412") }},
	} {
		t.Run(tc.path, func(t *testing.T) {
			reader, err := NewRunReader(EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "private-capability", nil }, HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.URL.RequestURI() != tc.path || req.Header.Get("Authorization") != "Bearer private-capability" {
					t.Fatalf("request %s %s", req.Method, req.URL)
				}
				return engineResponse(200, tc.body), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := tc.call(reader)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(got)
			if err != nil || strings.Contains(string(encoded), "node_id") {
				t.Fatalf("unsafe observation: %s %v", encoded, err)
			}
			if receipt, ok := got.(controllerwire.Receipt); ok && (receipt.ReceiptSHA != "sha256:original" || receipt.Cost.ComputeCents != 27 || !receipt.Cost.Settled) {
				t.Fatalf("receipt changed: %+v", receipt)
			}
		})
	}
}

func TestRunReaderRefusesInvalidQueriesBeforeTransport(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid request reached transport")
		return nil, nil
	})
	for _, q := range []controllerwire.RunQuery{{}, {Limit: -1}, {Limit: 1, ParentRunID: ".."}} {
		if _, err := c.ListRuns(t.Context(), q); err == nil {
			t.Fatalf("query accepted: %+v", q)
		}
	}
	for _, id := range []string{"", "..", "a\nb"} {
		if _, err := c.ReadSteps(t.Context(), id); err == nil {
			t.Fatalf("steps selector accepted: %q", id)
		}
		if _, err := c.ReadReceipt(t.Context(), id); err == nil {
			t.Fatalf("receipt selector accepted: %q", id)
		}
	}
}

func TestRunReaderRejectsMalformedPageAndPrivateSteps(t *testing.T) {
	for _, body := range []string{
		`{"runs":[],"truncated":false}`,
		`{"runs":[],"next_cursor":"","truncated":true}`,
		`{"runs":[],"next_cursor":"same","truncated":true}`,
		`{"runs":[],"next_cursor":"next","truncated":false}`,
		`{"runs":[{"id":"r1"}],"next_cursor":"","truncated":false}`,
	} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 1, Cursor: "same"}); err == nil {
			t.Fatalf("malformed page accepted: %s", body)
		}
	}
	c := testEngine(t, func(*http.Request) (*http.Response, error) {
		return engineResponse(200, `{"steps":[{"node_id":"j1","id":"s1","label":"s","attempt":1,"status":"finished"}]}`), nil
	})
	if _, err := c.ReadSteps(t.Context(), "r1"); err == nil {
		t.Fatal("old node step accepted")
	}
}
