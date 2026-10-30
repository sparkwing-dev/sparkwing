package controller

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

// TokenRequestBudget bounds authorized capability requests and their aggregate rate.
type TokenRequestBudget struct {
	// PerTokenMinute caps authorized requests per team or operator token.
	PerTokenMinute int

	// AlarmPerMinute triggers operator warnings for the aggregate authorized request rate.
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
		writeRetryAfter(w, wait, fmt.Sprintf("token request limit is %d per minute; current budget usage is at least %d requests; wait for Retry-After or ask the operator to raise the per-token request budget", t.policy.PerTokenMinute, t.policy.PerTokenMinute))
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
