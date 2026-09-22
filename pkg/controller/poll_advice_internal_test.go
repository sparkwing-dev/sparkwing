package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newAdviceServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, nil)
}

func TestClaimPollAdvice_WidensWithTheIdleWindow(t *testing.T) {
	srv := newAdviceServer(t)
	now := time.Now()

	for _, tc := range []struct {
		idle time.Duration
		want time.Duration
	}{
		{idle: 0, want: 0},
		{idle: 4 * time.Second, want: 0},
		{idle: 12 * time.Second, want: 3 * time.Second},
		{idle: 16 * time.Second, want: 4 * time.Second},
		{idle: 10 * time.Minute, want: DefaultMaxIdleClaimPoll},
	} {
		srv.recordQueueActivity(now.Add(-tc.idle))
		if got := srv.claimPollAdvice(now); got != tc.want {
			t.Errorf("idle %s: advice=%s want %s", tc.idle, got, tc.want)
		}
	}
}

func TestClaimPollAdvice_ZeroCeilingSuggestsNothing(t *testing.T) {
	srv := newAdviceServer(t).WithIdleClaimPoll(0)
	srv.recordQueueActivity(time.Now().Add(-time.Hour))
	if got := srv.claimPollAdvice(time.Now()); got != 0 {
		t.Errorf("advice=%s want 0", got)
	}
}

func TestHandleClaimNode_NamesAPollIntervalWhileIdle(t *testing.T) {
	srv := newAdviceServer(t)
	srv.recordQueueActivity(time.Now().Add(-5 * time.Minute))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/claim",
		strings.NewReader(`{"holder_id":"runner-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", rec.Code)
	}
	if got := rec.Header().Get(store.ClaimPollAfterHeader); got != "5" {
		t.Errorf("%s=%q want \"5\"", store.ClaimPollAfterHeader, got)
	}
}

func TestClaimPollAdvice_WorkArrivingClearsTheAdvice(t *testing.T) {
	srv := newAdviceServer(t)
	srv.recordQueueActivity(time.Now().Add(-5 * time.Minute))

	idle := httptest.NewRecorder()
	srv.Handler().ServeHTTP(idle, claimNodeRequest())
	if got := idle.Header().Get(store.ClaimPollAfterHeader); got == "" {
		t.Fatal("an idle controller suggested nothing; the ramp never started")
	}

	if err := srv.store.CreateTrigger(context.Background(), store.Trigger{
		ID: "run-arrived", Pipeline: "build", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed trigger: %v", err)
	}
	tenant, err := srv.store.ForTeam(context.Background(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.admitTrigger(context.Background(), tenant, triggerIntake{
		RunID: "run-admitted", Pipeline: "build", Source: "api", At: time.Now(),
	}); err != nil {
		t.Fatalf("admit trigger: %v", err)
	}

	fresh := httptest.NewRecorder()
	srv.Handler().ServeHTTP(fresh, claimNodeRequest())
	if got := fresh.Header().Get(store.ClaimPollAfterHeader); got != "" {
		t.Errorf("%s=%q after work arrived; a filled queue must return the fleet to its own cadence",
			store.ClaimPollAfterHeader, got)
	}
}

func claimNodeRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/claim",
		strings.NewReader(`{"holder_id":"runner-1"}`))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestClaimPollAdvice_LocalExecutionSuggestsNothing(t *testing.T) {
	srv := newAdviceServer(t).WithLocalExecution()
	srv.recordQueueActivity(time.Now().Add(-time.Hour))
	if got := srv.claimPollAdvice(time.Now()); got != 0 {
		t.Errorf("advice=%s want 0 on a host's own controller", got)
	}
}

func TestHandleClaimTrigger_WithholdsAdviceRightAfterAnAward(t *testing.T) {
	srv := newAdviceServer(t)
	srv.recordQueueActivity(time.Now())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/triggers/claim", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", rec.Code)
	}
	if got := rec.Header().Get(store.ClaimPollAfterHeader); got != "" {
		t.Errorf("%s=%q want no header", store.ClaimPollAfterHeader, got)
	}
}
