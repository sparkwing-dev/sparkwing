package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func serveEventsStreamWithRunErr(t *testing.T, ctx context.Context, runErr error) *httptest.ResponseRecorder {
	t.Helper()
	b := &fakeBackend{getRun: func(string) (*store.Run, error) { return nil, runErr }}
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/runs/run-1/events/stream", nil)
	rec := httptest.NewRecorder()
	serveEventsStream(b, rec, req, "run-1")
	return rec
}

func requireRetryableUnavailable(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"code":"unavailable"`) {
		t.Fatalf("got status %d Retry-After=%q body %q, want a retryable 503", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}

func TestEventsStreamAnswersRetryableUnavailableWhenTheRunReadIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	requireRetryableUnavailable(t, serveEventsStreamWithRunErr(t, ctx, context.Canceled))
}

func TestEventsStreamAnswersRetryableUnavailableWhenStoreIsBusy(t *testing.T) {
	for name, runErr := range map[string]error{
		"sqlite busy":    teststore.SQLiteBusyError(t),
		"store deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			requireRetryableUnavailable(t, serveEventsStreamWithRunErr(t, t.Context(), runErr))
		})
	}
}

func TestEventsStreamAnswersInternalErrorForOtherStoreFailures(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, ctx := range map[string]context.Context{"live request": t.Context(), "cancelled request": cancelled} {
		t.Run(name, func(t *testing.T) {
			rec := serveEventsStreamWithRunErr(t, ctx, errors.New("malformed run row"))
			if rec.Code != http.StatusInternalServerError || rec.Header().Get("Retry-After") != "" {
				t.Fatalf("got status %d Retry-After=%q body %q, want 500", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
			}
		})
	}
}
