package controller

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// safety: the binding check is the router's last step before the handler reads the run, so cancelling there
// cancels exactly that read, as a stopping controller's base context would.
type cancelAtBinding struct {
	capability.BindingChecker
	cancel context.CancelFunc
}

func (c cancelAtBinding) CheckBinding(ctx context.Context, p capability.Principal, rule capability.BindingRule, target capability.Target) error {
	c.cancel()
	return c.BindingChecker.CheckBinding(ctx, p, rule, target)
}

func serveEventsStreamRoute(t *testing.T, f scheduleHTTPFixture, ctx context.Context, run string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/runs/"+run+"/events/stream", nil)
	r.Header.Set("Authorization", "Bearer "+f.raw)
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	return w
}

func requireRetryableUnavailable(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), `"code":"unavailable"`) {
		t.Fatalf("got HTTP %d Retry-After=%q body %q, want a retryable %d", w.Code, w.Header().Get("Retry-After"), w.Body.String(), status)
	}
}

func TestEventsStreamRouteAnswersRetryableUnavailableAndLogsBelowErrorWhenTheRunReadIsCancelled(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	if err := teststore.SeedRun(f.s.store, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	f.s.logger = slog.New(slog.NewTextHandler(&log, nil))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.s.WithAuthorization(routepolicy.Authorization{Authenticator: capability.NewAuthenticator(f.facts), Bindings: cancelAtBinding{BindingChecker: f.facts, cancel: cancel}})
	w := serveEventsStreamRoute(t, f, ctx, "run-1")
	if strings.Contains(log.String(), "level=ERROR") || !strings.Contains(log.String(), "context canceled") {
		t.Fatalf("a cancelled run read must log its cause below ERROR; log:\n%s", log.String())
	}
	requireRetryableUnavailable(t, w, http.StatusServiceUnavailable)
}

func TestEventsStreamRouteAnswersRetryableUnavailableWhenTheRunLookupFindsTheStoreBusy(t *testing.T) {
	f := newScheduleHTTPFixture(t)
	f.s.WithEngineController(&EngineController{ResolveTenant: selectorTenant(store.DefaultTeam, teststore.SQLiteBusyError(t))})
	requireRetryableUnavailable(t, serveEventsStreamRoute(t, f, t.Context(), "run-1"), http.StatusServiceUnavailable)
}

func TestEngineLookupErrorDoesNotTellAWriteToRetryAfterAStoreConflict(t *testing.T) {
	// safety: write routes share engineLookupError for reads after their commit, where a retry could repeat the write.
	var failure *apierr.Error
	if err := engineLookupError(teststore.SQLiteBusyError(t)); errors.As(err, &failure) && failure.Code == hosterr.Unavailable {
		t.Fatalf("engineLookupError answered a SQLite conflict as retryable: %v", err)
	}
}
