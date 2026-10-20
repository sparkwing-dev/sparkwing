package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func newAdviceServer(t *testing.T) *Server {
	t.Helper()
	engine, _ := engineRuntimeStore(t)
	engine.claimProfiles = engineProfileFake{}
	engine.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	engineHTTPRoot(t, engine, "a", "advice", []capability.Grant{capability.NodesClaim})
	return New(engine.Store, nil).WithEngineController(engine.EngineController)
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

	req := claimNodeRequest()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
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

	srv.recordQueueActivity(time.Now())

	fresh := httptest.NewRecorder()
	srv.Handler().ServeHTTP(fresh, claimNodeRequest())
	if got := fresh.Header().Get(store.ClaimPollAfterHeader); got != "" {
		t.Errorf("%s=%q after work arrived; a filled queue must return the fleet to its own cadence",
			store.ClaimPollAfterHeader, got)
	}
}

func claimNodeRequest() *http.Request { return namedClaimRequest("runner-1") }

func TestHandleClaimTrigger_WithholdsAdviceRightAfterAnAward(t *testing.T) {
	srv := newAdviceServer(t)
	srv.recordQueueActivity(time.Now())

	req := claimNodeRequest()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 204", rec.Code)
	}
	if got := rec.Header().Get(store.ClaimPollAfterHeader); got != "" {
		t.Errorf("%s=%q want no header", store.ClaimPollAfterHeader, got)
	}
}
