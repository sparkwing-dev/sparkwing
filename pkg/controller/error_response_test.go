package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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

func TestAgentRoutesMaskStoreFailures(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
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
		{name: "agents", method: http.MethodGet, path: "/api/v1/agents"},
		{name: "claim", method: http.MethodPost, path: "/api/v1/nodes/claim", body: `{"holder_id":"private-url.invalid"}`},
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
