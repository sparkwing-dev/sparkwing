package controller

import (
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
		{idle: 40 * time.Second, want: 10 * time.Second},
		{idle: 10 * time.Minute, want: DefaultMaxIdleClaimPoll},
	} {
		srv.recordClaimAward(now.Add(-tc.idle))
		if got := srv.claimPollAdvice(now); got != tc.want {
			t.Errorf("idle %s: advice=%s want %s", tc.idle, got, tc.want)
		}
	}
}

func TestClaimPollAdvice_ZeroCeilingSuggestsNothing(t *testing.T) {
	srv := newAdviceServer(t).WithIdleClaimPoll(0)
	srv.recordClaimAward(time.Now().Add(-time.Hour))
	if got := srv.claimPollAdvice(time.Now()); got != 0 {
		t.Errorf("advice=%s want 0", got)
	}
}

func TestHandleClaimNode_NamesAPollIntervalWhileIdle(t *testing.T) {
	srv := newAdviceServer(t)
	srv.recordClaimAward(time.Now().Add(-5 * time.Minute))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/claim",
		strings.NewReader(`{"holder_id":"runner-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204", rec.Code)
	}
	if got := rec.Header().Get(store.ClaimPollAfterHeader); got != "15" {
		t.Errorf("%s=%q want \"15\"", store.ClaimPollAfterHeader, got)
	}
}

func TestClaimPollAdvice_LocalExecutionSuggestsNothing(t *testing.T) {
	srv := newAdviceServer(t).WithLocalExecution()
	srv.recordClaimAward(time.Now().Add(-time.Hour))
	if got := srv.claimPollAdvice(time.Now()); got != 0 {
		t.Errorf("advice=%s want 0 on a host's own controller", got)
	}
}

func TestHandleClaimTrigger_WithholdsAdviceRightAfterAnAward(t *testing.T) {
	srv := newAdviceServer(t)
	srv.recordClaimAward(time.Now())

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
