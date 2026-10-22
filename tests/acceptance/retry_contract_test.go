package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestRetrySelectionContract(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		for flag, mode := range map[string]controllerwire.RetryMode{"failed": controllerwire.RetryFailed, "all-jobs": controllerwire.RetryAll} {
			t.Run(endpoint+"/"+flag, func(t *testing.T) {
				if _, err := clisurface.Parse("sparkwing", []string{"runs", "retry", "--run", "412", "--" + flag}); err != nil {
					t.Fatal(err)
				}
				calls := 0
				c, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != "POST" || req.URL.Path != "/api/v1/runs/412/retry" || req.URL.RawQuery != "" || req.Header.Get("Idempotency-Key") != "retry-selection" || req.Header.Get("Authorization") != "Bearer member-authority" {
						t.Fatalf("wrong retry request: %s %s", req.Method, req.URL)
					}
					var body map[string]json.RawMessage
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil || len(body) != 2 || string(body["mode"]) != `"`+string(mode)+`"` || string(body["idempotency_key"]) != `"retry-selection"` {
						t.Fatalf("retry proposal: %s %v", body, err)
					}
					response, err := json.Marshal(controllerwire.CreateRunResponse{RunID: "retry-run", Number: 413, Inputs: runspec.Display{Pipeline: "deploy"}, Replayed: true})
					if err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(string(response)))}, nil
				})}, Bearer: func(context.Context) (string, error) { return "member-authority", nil }})
				if err != nil {
					t.Fatal(err)
				}
				got, err := c.Retry(t.Context(), "412", controllerwire.RetryRequest{Mode: mode, IdempotencyKey: "retry-selection"})
				if err != nil || got.RunID != "retry-run" || got.Number != 413 || got.Inputs.Pipeline != "deploy" || !got.Replayed || calls != 1 {
					t.Fatalf("retry receipt: run=%q number=%d replayed=%v calls=%d error=%v", got.RunID, got.Number, got.Replayed, calls, err)
				}
			})
		}
	}
}

func TestRetryRejectsUnspecifiedModeBeforeTransport(t *testing.T) {
	c, err := client.NewEngineClient(client.EngineOptions{URL: "http://api.sock", HTTP: &http.Client{Transport: surfaceTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid retry reached transport")
		return nil, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []controllerwire.RetryRequest{
		{}, {IdempotencyKey: "key"}, {Mode: "full", IdempotencyKey: "key"}, {Mode: controllerwire.RetryAll},
	} {
		if _, err := c.Retry(t.Context(), "412", input); err == nil {
			t.Fatalf("invalid retry accepted: %+v", input)
		}
	}
}
