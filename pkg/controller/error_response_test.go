package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestWriteErrorMasksInternalDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	private := "sql: users.token_prefix swtoken-private /private/db https://private-controller.invalid"
	writeError(rec, http.StatusInternalServerError, errors.New(private))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if body != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("body = %q", body)
	}
	for _, sentinel := range strings.Fields(private) {
		if strings.Contains(body, sentinel) {
			t.Errorf("internal response exposed %q: %s", sentinel, body)
		}
	}
}

func TestWriteExecutionAdmissionErrorPreservesUnresolvedSafeHold(t *testing.T) {
	rec := httptest.NewRecorder()
	err := &executionpolicy.UpgradeRequiredError{
		Scope: "supervisor", Missing: []string{"future-supervisor-v9"}, SafeHold: true,
	}
	if !writeExecutionAdmissionError(rec, err) {
		t.Fatal("typed upgrade error was not handled")
	}
	var body executionAdmissionErrorBody
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if rec.Code != http.StatusConflict || body.Code != "upgrade_required" || !body.SafeHold ||
		body.MinimumRelease != "" || len(body.Missing) != 1 || body.Missing[0] != "future-supervisor-v9" {
		t.Fatalf("safe-hold response = status %d, body %+v", rec.Code, body)
	}
}

func TestResetNodeForAutoRetryMasksStoreFailure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, nil).Handler()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-private/nodes/node-private/auto-retry/reset", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestAssistedRoutesMaskStoreFailures(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, nil).Handler()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, method, path, body string
	}{
		{name: "fleet", method: http.MethodGet, path: "/api/v1/agents"},
		{name: "heartbeat", method: http.MethodPost, path: "/api/v1/agents/private-path/heartbeat", body: `{"headroom":{"cores":1,"memory_bytes":1,"queue_depth":0}}`},
		{name: "legacy claim", method: http.MethodPost, path: "/api/v1/nodes/claim", body: `{"holder_id":"private-url.invalid"}`},
		{name: "offer prepare", method: http.MethodPost, path: "/api/v1/nodes/claim/prepare", body: `{"executor_name":"private-token-prefix"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if body := rec.Body.String(); body != "{\"error\":\"internal server error\"}\n" {
				t.Fatalf("body = %q", body)
			}
		})
	}
}
