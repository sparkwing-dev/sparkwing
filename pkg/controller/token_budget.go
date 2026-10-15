package controller

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TokenRequestBudget bounds ordinary authenticated requests and signed-data
// requests, and names the controller-wide rate an operator wants to hear about.
// Signed-up teams share a bucket; the operator's team uses a bucket per token.
// The per-runner budgets in [RequestBudget] size a cooperating runner's loops;
// this budget still bounds a caller varying the runner it claims to be.
//
// Zero in either field leaves that guard off, which is what a controller
// starts with.
type TokenRequestBudget struct {
	// PerTokenMinute caps requests from one signed-up team or operator token
	// per rolling minute, including signed-data requests made with claim grants.
	// Past it a request is answered 429 with a Retry-After naming the refill
	// delay. A caller carrying no token uses its client address.
	PerTokenMinute int

	// AlarmPerMinute is the request rate, counted across every caller, past
	// which the controller logs at warn and counts an alarm. It refuses
	// nothing: it is the operator's notice that one pod is serving more than
	// it was sized for.
	AlarmPerMinute int
}

// WithTokenRequestBudget installs b as the controller request budget and
// controller-wide rate alarm. Calling it with the zero budget turns both off.
func (s *Server) WithTokenRequestBudget(b TokenRequestBudget) *Server {
	s.tokenBudget = newTokenBudget(b)
	return s
}

const budgetClassToken = "token"

type tokenBudget struct {
	policy   TokenRequestBudget
	requests *ratelimit.Limiter
	alarm    *rateAlarm
}

func newTokenBudget(b TokenRequestBudget) *tokenBudget {
	t := &tokenBudget{policy: b}
	if b.PerTokenMinute > 0 {
		t.requests = ratelimit.New(b.PerTokenMinute, budgetWindow)
	}
	if b.AlarmPerMinute > 0 {
		t.alarm = &rateAlarm{limit: b.AlarmPerMinute, window: budgetWindow}
	}
	return t
}

func (t *tokenBudget) values() TokenRequestBudget {
	if t == nil {
		return TokenRequestBudget{}
	}
	return t.policy
}

func (s *Server) allowDataRequest(w http.ResponseWriter, r *http.Request, team store.Team, prefix string) bool {
	principal := &Principal{Team: team, TokenPrefix: prefix}
	// safety: this partial principal only keys the budget, so it stays out of
	// the audit record that names the credential the caller authenticated.
	key := s.floodKey(r.WithContext(context.WithValue(r.Context(), principalCtxKey{}, principal)), "")
	return s.allowTokenRequest(w, r, key)
}

func (s *Server) allowTokenRequest(w http.ResponseWriter, r *http.Request, key string) bool {
	t := s.tokenBudget
	if t == nil {
		return true
	}
	now := time.Now()
	if t.alarm != nil && t.alarm.record(now) {
		observeRequestRateAlarm()
		s.logger.Warn("controller request rate above its alarm",
			"requests_per_minute", t.policy.AlarmPerMinute,
			"reason", "one controller is serving more requests a minute than it was sized for")
	}
	if t.requests == nil {
		return true
	}
	allowed, wait := t.requests.AllowWithRetry(key, now)
	if !allowed {
		observePrincipalThrottled(budgetClassToken)
		s.logger.Warn("request shed",
			"principal", key, "route_class", budgetClassToken, "retry_after", wait,
			"reason", "per-token request budget exhausted")
		writeRetryAfter(w, wait, "too many requests from this token")
		return false
	}
	return true
}

// safety: an alarm the operator hears once a minute is a notice; one raised per
// request over the line is the flood it is reporting, so each window says so once.
type rateAlarm struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	started time.Time
	count   int
	raised  bool
}

func (a *rateAlarm) record(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started.IsZero() || now.Sub(a.started) >= a.window {
		a.started = now
		a.count = 0
		a.raised = false
	}
	a.count++
	if a.raised || a.count < a.limit {
		return false
	}
	a.raised = true
	return true
}
