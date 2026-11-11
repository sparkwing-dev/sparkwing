package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/apierrhttp"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestUnauthenticatedReasonStaysInAudit(t *testing.T) {
	credential := "swk_" + strings.Repeat("x", 43)
	failure := &apierr.Error{Code: hosterr.Unauthenticated, Message: "authority has ended " + credential}
	handler, logs := auditHarness(t, "/api/v1/runs/{run}", func(w http.ResponseWriter, _ *http.Request) {
		routepolicy.WritePolicyError(w, failure)
	})
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run", nil))
	if out.Code != 401 || strings.Contains(out.Body.String(), "ended") || strings.Contains(out.Body.String(), credential) {
		t.Fatalf("public refusal: %d %s", out.Code, out.Body)
	}
	lines := auditLines(t, logs, "audit")
	if len(lines) != 1 {
		t.Fatalf("audit count=%d", len(lines))
	}
	reason, _ := lines[0]["reason"].(string)
	if !strings.Contains(reason, "authority has ended") || strings.Contains(logs.String(), credential) {
		t.Fatalf("private reason missing or unsafe: %s", logs)
	}
}

func TestUnauthenticatedAuditUsesRefusalInsteadOfEarlierFailure(t *testing.T) {
	handler, logs := auditHarness(t, "/api/v1/runs/{run}", func(w http.ResponseWriter, _ *http.Request) {
		apierrhttp.NoteResponseError(w, errors.New("earlier handler failure"))
		routepolicy.WritePolicyError(w, &apierr.Error{Code: hosterr.Unauthenticated, Message: "authority has ended"})
	})
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run", nil))
	lines := auditLines(t, logs, "audit")
	if out.Code != http.StatusUnauthorized || len(lines) != 1 || lines[0]["reason"] != "authority has ended" {
		t.Fatalf("audit did not retain the issued refusal: status=%d logs=%s", out.Code, logs)
	}
}
