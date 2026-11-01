package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/health"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestHealthDatabaseRetryLogLevel(t *testing.T) {
	dns := fmt.Errorf("connect database: %w", &net.DNSError{Err: "no such host", Name: "database", IsNotFound: true})
	timeout := &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "timeout", IsTimeout: true}}
	fault := errors.New("database authentication failed")
	for _, tc := range []struct {
		name     string
		failures []error
		canceled bool
		status   int
	}{
		{"healthy", []error{nil}, false, http.StatusOK},
		{"DNS recovered", []error{dns, nil}, false, http.StatusOK},
		{"timeout recovered", []error{timeout, nil}, false, http.StatusOK},
		{"DNS exhausted", []error{dns, dns}, false, http.StatusServiceUnavailable},
		{"retry fault", []error{dns, fault}, false, http.StatusServiceUnavailable},
		{"real fault", []error{fault}, false, http.StatusServiceUnavailable},
		{"canceled request", []error{dns}, true, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			calls := 0
			s := New(nil, slog.New(slog.DiscardHandler))
			h, logs := auditHarness(t, "/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
				err := pingHealthDatabase(r.Context(), func(context.Context) error {
					if calls >= len(tc.failures) {
						t.Fatal("unexpected database retry")
					}
					err := tc.failures[calls]
					calls++
					return err
				})
				if err != nil {
					apierr.NoteResponseError(w, s.healthDatabaseResult(r.Context(), err))
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			})
			out := httptest.NewRecorder()
			h.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil).WithContext(ctx))
			if out.Code != tc.status || calls != len(tc.failures) {
				t.Fatalf("status=%d calls=%d, want status=%d calls=%d", out.Code, calls, tc.status, len(tc.failures))
			}
			diagnostics := auditLines(t, logs, "http request failed")
			if tc.status == http.StatusOK {
				if len(diagnostics) != 0 {
					t.Fatalf("recovered health failure logged: %s", logs)
				}
			} else if len(diagnostics) != 1 || diagnostics[0]["level"] != "WARN" || diagnostics[0]["err"] != "controller health database check: "+tc.failures[len(tc.failures)-1].Error() {
				t.Fatalf("single health failure must log once at WARN with cause: %s", logs)
			}
		})
	}
}

func TestHealthFailureLoggingAndRecovery(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var logs bytes.Buffer
	s := New(st, slog.New(slog.NewJSONHandler(&logs, nil)))
	h := s.Handler()
	probe := func(status int) {
		t.Helper()
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		if out.Code != status {
			t.Fatalf("health status = %d, want %d: %s", out.Code, status, out.Body)
		}
		if status == http.StatusServiceUnavailable {
			var body health.Response
			if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != "degraded" || body.Auth != "enabled" || len(body.Problems) != 1 || body.Problems[0] != "db: unavailable" {
				t.Fatalf("health failure response changed: %s", out.Body)
			}
		}
	}
	probe(http.StatusOK)
	if len(auditLines(t, &logs, "controller health database recovered")) != 0 {
		t.Fatal("healthy startup logged a recovery")
	}
	failed, err := teststore.Open(filepath.Join(t.TempDir(), "failed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.Close(); err != nil {
		t.Fatal(err)
	}
	for _, failures := range []int{1, 16, 16} {
		s.operator = failed.AsOperator()
		for i := range failures {
			logs.Reset()
			probe(http.StatusServiceUnavailable)
			records := auditLines(t, &logs, "http request failed")
			if i >= 12 {
				if len(records) != 0 {
					t.Fatalf("sustained outage repeated its diagnostic: %s", &logs)
				}
				continue
			}
			level := "WARN"
			if i == 11 {
				level = "ERROR"
			}
			if len(records) != 1 || records[0]["level"] != level || records[0]["err"] != "controller health database check: sql: database is closed" {
				t.Fatalf("probe %d: want one %s with cause: %s", i+1, level, &logs)
			}
		}
		s.operator = st.AsOperator()
		logs.Reset()
		probe(http.StatusOK)
		probe(http.StatusOK)
		records := auditLines(t, &logs, "controller health database recovered")
		if len(records) != 1 || records[0]["level"] != "INFO" {
			t.Fatalf("recovery must log INFO once: %s", &logs)
		}
	}
}
