package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestIdlePollGate_RefusesUntilTheSuggestedIntervalHasPassed(t *testing.T) {
	gate := &idlePollGate{marks: make(map[string]idlePollMark)}
	start := time.Now()
	awarded := start.Add(-time.Minute)
	gate.record("runner-1", 5*time.Second, start)

	if wait, refused := gate.check("runner-1", start.Add(time.Second), awarded); !refused || wait != 4*time.Second {
		t.Errorf("one second in: wait=%s refused=%v; want a 4s wait", wait, refused)
	}
	if _, refused := gate.check("runner-1", start.Add(5*time.Second), awarded); refused {
		t.Error("a runner that waited the whole interval was still refused")
	}
	if _, refused := gate.check("runner-2", start.Add(time.Second), awarded); refused {
		t.Error("a runner that was never suggested an interval was refused")
	}
}

func TestIdlePollGate_WorkHandedOutLiftsTheRefusal(t *testing.T) {
	gate := &idlePollGate{marks: make(map[string]idlePollMark)}
	start := time.Now()
	gate.record("runner-1", 5*time.Second, start)

	if _, refused := gate.check("runner-1", start.Add(time.Second), start.Add(500*time.Millisecond)); refused {
		t.Error("a runner was held off a queue that filled after the suggestion went out")
	}
}

func TestClaimPollAdvice_EnforcementRefusesAnEarlyPoll(t *testing.T) {
	srv := newAdviceServer(t).WithIdleClaimPollEnforced(true)
	srv.recordQueueActivity(time.Now().Add(-5 * time.Minute))

	advised := httptest.NewRecorder()
	srv.Handler().ServeHTTP(advised, claimNodeRequest())
	if advised.Code != http.StatusNoContent {
		t.Fatalf("first claim: status=%d want 204", advised.Code)
	}
	if got := advised.Header().Get(store.ClaimPollAfterHeader); got != "5" {
		t.Fatalf("%s=%q want \"5\"", store.ClaimPollAfterHeader, got)
	}

	early := httptest.NewRecorder()
	srv.Handler().ServeHTTP(early, claimNodeRequest())
	if early.Code != http.StatusTooManyRequests {
		t.Fatalf("early claim: status=%d want 429", early.Code)
	}
	if got := early.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After=%q want \"5\"", got)
	}
}

func TestClaimPollAdvice_EnforcementIsOffUntilAskedFor(t *testing.T) {
	srv := newAdviceServer(t)
	srv.recordQueueActivity(time.Now().Add(-5 * time.Minute))

	for attempt := range 2 {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, claimNodeRequest())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("claim %d: status=%d want 204 on a controller that enforces nothing", attempt, rec.Code)
		}
	}
}

func TestComputeLimits_ReportsTheBudgetsInForce(t *testing.T) {
	srv := newAdviceServer(t).
		WithRequestBudget(RequestBudget{ClaimsPerMinute: 9600, HeartbeatsPerMinute: 1200}).
		WithIdleClaimPollEnforced(true).
		WithEgressMeter(egress.New(egress.Config{
			MaxStreamsPerPrincipal: 50, MaxDownloadsPerPrincipal: 20,
		}))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/compute-limits", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200: %s", rec.Code, rec.Body.String())
	}
	var view computeLimitsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := requestBudgetsJSON{
		ClaimsPerRunnerMinute:     9600,
		HeartbeatsPerRunnerMinute: 1200,
		IdleClaimPollSeconds:      int64(DefaultMaxIdleClaimPoll / time.Second),
		IdleClaimPollEnforced:     true,
		MaxLogStreamsPerPrincipal: 50,
		MaxDownloadsPerPrincipal:  20,
	}
	if view.Budgets != want {
		t.Errorf("budgets = %+v; want %+v", view.Budgets, want)
	}
}

func TestComputeLimits_ReportsNoBudgetOnAnInstallThatSetsNone(t *testing.T) {
	srv := newAdviceServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/compute-limits", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200: %s", rec.Code, rec.Body.String())
	}
	var view computeLimitsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := requestBudgetsJSON{IdleClaimPollSeconds: int64(DefaultMaxIdleClaimPoll / time.Second)}
	if view.Budgets != want {
		t.Errorf("budgets = %+v; want %+v", view.Budgets, want)
	}
}
