package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func auditHarness(t *testing.T, route string, h http.HandlerFunc) (http.Handler, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	return withRequestLog(h, logger, func(*http.Request) string { return route },
		func(*http.Request) string { return "192.0.2.1" }), &logs
}

func auditLines(t *testing.T, logs *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// A write whose handler panics is still audited, as a 500, and the panic
// still reaches the server.
func TestAuditRecordsAPanickedWrite(t *testing.T) {
	h, logs := auditHarness(t, "/api/v1/secrets", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not reach the server")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/secrets", nil))
	}()
	records := auditLines(t, logs, "audit")
	if len(records) != 1 || records[0]["status"] != float64(http.StatusInternalServerError) {
		t.Fatalf("audit after a panic = %v, want one 500 record", records)
	}
}

// An executor offer answers the node claim route with 204 while it records
// a pending offer, so that answer is audited; an empty poll is not.
func TestAuditKeepsAPendingExecutorOffer(t *testing.T) {
	h, logs := auditHarness(t, "/api/v1/nodes/claim", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offer") != "" {
			w.Header().Set(claimOfferStateHeader, "pending")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/nodes/claim", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/nodes/claim?offer=1", nil))
	if records := auditLines(t, logs, "audit"); len(records) != 1 {
		t.Fatalf("audit records = %v, want only the pending offer", records)
	}
}

// The partial principal a data route builds to key its request budget does
// not replace the credential the caller authenticated with.
func TestAuditNamesTheCredentialNotTheBudgetKey(t *testing.T) {
	s := New(nil, slog.New(slog.DiscardHandler))
	h, logs := auditHarness(t, "/api/v1/data/uploads", func(w http.ResponseWriter, r *http.Request) {
		noteAuditPrincipal(r.Context(), &Principal{Name: "pat@example.com", Kind: "user", AccountID: "acct-1", Team: "acme"})
		s.allowDataRequest(w, r, "acme", "swk_budget")
		w.WriteHeader(http.StatusCreated)
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/data/uploads", nil))
	records := auditLines(t, logs, "audit")
	if len(records) != 1 || records[0]["principal_id"] != "acct-1" || records[0]["principal_kind"] != "user" {
		t.Fatalf("audit = %v, want the authenticated account", records)
	}
	if strings.Contains(logs.String(), "pat@example.com") {
		t.Fatalf("audit carries the account's email: %s", logs.String())
	}
}

// An internal error names the request by its id and route pattern, never the
// raw path, which can name a secret.
func TestInternalErrorLogNamesTheRouteNotThePath(t *testing.T) {
	var logs bytes.Buffer
	s := New(nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	h := withRequestLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.writeInternalError(w, r, "read secret", errors.New("disk gone"))
	}), slog.New(slog.DiscardHandler), func(*http.Request) string { return "/api/v1/secrets/{name}" },
		func(*http.Request) string { return "" })
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/secrets/PROD_DB_PASSWORD", nil))
	records := auditLines(t, &logs, "read secret")
	if len(records) != 1 || records[0]["route"] != "/api/v1/secrets/{name}" || records[0]["request_id"] == "" {
		t.Fatalf("error log = %v, want the route and request id", records)
	}
	if strings.Contains(logs.String(), "PROD_DB_PASSWORD") {
		t.Fatalf("error log carries the raw path: %s", logs.String())
	}
}
