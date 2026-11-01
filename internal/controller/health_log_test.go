package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
					apierr.NoteResponseError(w, fmt.Errorf("controller health database check: %w", err))
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
			} else if len(diagnostics) != 1 || diagnostics[0]["level"] != "ERROR" {
				t.Fatalf("unrecovered health failure must log once at ERROR: %s", logs)
			}
		})
	}
}
