package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the gate only speaks once the controller has been suggesting its
// widest interval for a whole interval, so an idle window shorter than that
// leaves a test measuring nothing.
const testIdleWindow = 5 * time.Minute

func namedClaimRequest(runner string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/claim",
		strings.NewReader(`{"holder_id":"`+runner+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(store.RunnerIdentityHeader, runner)
	return req
}

func fromClient(req *http.Request, addr string) *http.Request {
	req.RemoteAddr = addr
	return req
}

func TestClaimPollAdvice_EnforcementRefusesAnEarlyPoll(t *testing.T) {
	srv := newAdviceServer(t).WithIdleClaimPollEnforced(true)
	srv.recordQueueActivity(time.Now().Add(-testIdleWindow))

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
	// safety: never shorter than the real refill, and never longer than one
	// suggestion, or two refused polls would not fit inside the placement hold.
	if got := early.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After=%q, want the suggested interval", got)
	}
}

func TestClaimPollAdvice_EnforcementIsOffUntilAskedFor(t *testing.T) {
	srv := newAdviceServer(t)
	srv.recordQueueActivity(time.Now().Add(-testIdleWindow))

	for attempt := range 2 {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, claimNodeRequest())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("claim %d: status=%d want 204 on a controller that enforces nothing", attempt, rec.Code)
		}
	}
}

// TestClaimPollAdvice_EnforcementHoldsOffWhileTheSuggestionIsStillWidening
// pins the promise the docs make: a runner is never refused against an
// interval it was not yet told about.
func TestClaimPollAdvice_EnforcementHoldsOffWhileTheSuggestionIsStillWidening(t *testing.T) {
	srv := newAdviceServer(t).WithIdleClaimPollEnforced(true)
	// safety: four times the ceiling is the instant the suggestion first
	// reaches it, so the interval before this one was narrower.
	srv.recordQueueActivity(time.Now().Add(-4 * DefaultMaxIdleClaimPoll))

	for attempt := range 2 {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, claimNodeRequest())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("claim %d: status=%d, want 204 while the suggestion is still widening", attempt, rec.Code)
		}
	}
}

func TestClaimPollAdvice_WorkHandedOutLiftsTheRefusal(t *testing.T) {
	srv := newAdviceServer(t).WithIdleClaimPollEnforced(true)
	srv.recordQueueActivity(time.Now().Add(-testIdleWindow))

	first := httptest.NewRecorder()
	srv.Handler().ServeHTTP(first, claimNodeRequest())
	if first.Code != http.StatusNoContent {
		t.Fatalf("first claim: status=%d want 204", first.Code)
	}

	srv.recordQueueActivity(time.Now())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, claimNodeRequest())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204: a runner was held off a queue that has since filled", rec.Code)
	}
}

// TestClaimPollAdvice_TwoRunnersOnOneHostAreBothAdmitted covers a pool that
// runs two runner processes on one machine, where the holder prefix they are
// given is the hostname and only the process id tells them apart.
func TestClaimPollAdvice_TwoRunnersOnOneHostAreBothAdmitted(t *testing.T) {
	srv := newAdviceServer(t).WithIdleClaimPollEnforced(true)
	srv.recordQueueActivity(time.Now().Add(-testIdleWindow))

	for _, runner := range []string{"runner:host-a:11", "runner:host-a:12"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, namedClaimRequest(runner))
		if rec.Code != http.StatusNoContent {
			t.Errorf("%s: status=%d, want 204; two runners on one host are two runners", runner, rec.Code)
		}
	}
}

// TestClaimPollAdvice_AFloodOfRunnerNamesLeavesTheGateUp is the shape one
// token used to switch enforcement off fleet-wide with. The runner name on
// this route is the caller's own word, so a caller varying it gets a fresh
// gate each time; what must not happen is that it takes anybody else's with
// it, and what stops the flood itself is the per-token budget.
func TestClaimPollAdvice_AFloodOfRunnerNamesLeavesTheGateUp(t *testing.T) {
	const hostileBudget = 50
	srv := newAdviceServer(t).
		WithIdleClaimPollEnforced(true).
		WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: hostileBudget})
	srv.recordQueueActivity(time.Now().Add(-testIdleWindow))

	first := httptest.NewRecorder()
	srv.Handler().ServeHTTP(first, fromClient(namedClaimRequest("runner:real:1"), "192.0.2.1:9000"))
	if first.Code != http.StatusNoContent {
		t.Fatalf("first claim: status=%d want 204", first.Code)
	}

	shed := 0
	for i := range hostileBudget * 4 {
		rec := httptest.NewRecorder()
		req := fromClient(namedClaimRequest(fmt.Sprintf("runner-%d", i)), "198.51.100.1:9000")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			shed++
		}
	}
	if shed == 0 {
		t.Error("the flood spent no budget; a caller varying its runner name is bounded by its token")
	}

	early := httptest.NewRecorder()
	srv.Handler().ServeHTTP(early, fromClient(namedClaimRequest("runner:real:1"), "192.0.2.1:9000"))
	if early.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429; one caller's runner names switched enforcement off for another", early.Code)
	}
}

func TestRateAlarm_SpeaksOncePerWindow(t *testing.T) {
	alarm := &rateAlarm{limit: 2, window: time.Minute}
	start := time.Now()

	if alarm.record(start) {
		t.Error("the alarm spoke on the first request of a window")
	}
	if !alarm.record(start) {
		t.Error("the alarm stayed silent at its limit")
	}
	if alarm.record(start) {
		t.Error("the alarm spoke twice in one window")
	}
	if alarm.record(start.Add(time.Minute)) {
		t.Error("a fresh window opened above its limit")
	}
}

func TestComputeLimits_ReportsTheBudgetsInForce(t *testing.T) {
	srv := newAdviceServer(t).
		WithRequestBudget(RequestBudget{ClaimsPerMinute: 480, HeartbeatsPerMinute: 1200}).
		WithTokenRequestBudget(TokenRequestBudget{PerTokenMinute: 2000, AlarmPerMinute: 5000}).
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
		ClaimsPerRunnerMinute:     480,
		HeartbeatsPerRunnerMinute: 1200,
		IdleClaimPollSeconds:      int64(DefaultMaxIdleClaimPoll / time.Second),
		IdleClaimPollEnforced:     true,
		MaxLogStreamsPerPrincipal: 50,
		MaxDownloadsPerPrincipal:  20,
		RequestsPerTokenMinute:    2000,
		RequestsPerMinuteAlarm:    5000,
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
