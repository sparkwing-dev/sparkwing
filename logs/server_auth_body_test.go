package logs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// IMP-022: pin the wire shape that the logs service emits on 403.
// White-box test: drives requireScope directly with a logsPrincipal
// in context, so we don't have to spin up a fake controller for
// whoami auth.
func TestRequireScope_ForbiddenBodyShape(t *testing.T) {
	s := &Server{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // must not be reached
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs/run-1/step-a", nil)
	p := &logsPrincipal{
		Name:   "warm-runner-3",
		Kind:   "runner",
		Scopes: []string{scopeLogsRead}, // missing logs.write
	}
	req = req.WithContext(contextWithLogsPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()

	s.requireScope(scopeLogsWrite, inner).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	var body AuthErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v -- raw=%q", err, rec.Body.String())
	}
	if body.Error != "missing_scope" {
		t.Errorf("Error: got %q, want missing_scope", body.Error)
	}
	if body.MissingScope != scopeLogsWrite {
		t.Errorf("MissingScope: got %q, want %q", body.MissingScope, scopeLogsWrite)
	}
	if body.Principal != "runner:warm-runner-3" {
		t.Errorf("Principal: got %q, want runner:warm-runner-3", body.Principal)
	}
	if body.Message == "" {
		t.Errorf("Message must be non-empty")
	}
}
