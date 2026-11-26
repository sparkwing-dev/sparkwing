package controller

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

// TokenRequestBudget bounds authorized capability requests and their aggregate rate.
type TokenRequestBudget struct {
	// PerTokenMinute sizes the token bucket each team, or each teamless
	// authority, spends authorized requests from.
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

const (
	budgetClassToken       = "token"
	budgetClassInflight    = "inflight_reserve"
	inflightReserveDivisor = 8
)

func inflightReserve(perTokenMinute int) int {
	return max(perTokenMinute/inflightReserveDivisor, 1)
}

type tokenBudget struct {
	policy   TokenRequestBudget
	requests *ratelimit.Limiter
	inflight *ratelimit.Limiter
	alarm    *rateAlarm
}

func newTokenBudget(b TokenRequestBudget) *tokenBudget {
	t := &tokenBudget{policy: b}
	if b.PerTokenMinute > 0 {
		t.requests = ratelimit.New(b.PerTokenMinute, budgetWindow)
		t.inflight = ratelimit.New(inflightReserve(b.PerTokenMinute), budgetWindow)
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

func (s *Server) allowTokenRequest(w http.ResponseWriter, r *http.Request, a capability.Authorized, launcherRoute, inflightRoute bool) bool {
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
	// safety: the Cloud launcher claims and reconciles every team's Cloud Jobs under one credential, so a shared
	// bucket would let all teams' load stall placement and renewal. Only its claim and sync routes skip the
	// bucket, keeping a credential with other grants budgeted on their routes; the alarm above still counts it.
	if t.requests == nil || launcherRoute && capability.CloudLauncher(a.Principal.Capability) {
		return true
	}
	key := credentialBudgetKey(a.Principal)
	allowed, wait := t.requests.AllowWithRetry(key, now)
	if allowed {
		return true
	}
	if inflightRoute && a.LeaseBound() && a.Target.Team != "" {
		return s.allowInflightReserve(w, a.Target.Team, now)
	}
	observePrincipalThrottled(budgetClassToken)
	s.logger.Warn("request shed",
		"principal", key, "route_class", budgetClassToken, "retry_after", wait,
		"reason", "team or authority request budget exhausted")
	writeRetryAfter(w, wait, fmt.Sprintf("team or authority request limit is %d per minute; current budget usage is at least %d requests; wait for Retry-After or ask the operator to raise --requests-per-token-minute", t.policy.PerTokenMinute, t.policy.PerTokenMinute))
	return false
}

// safety: the reserve is keyed by the target team, not the budget key, so a team's leases share one reserve however
// many credentials hold them. A refusal charges nothing: debt here would let the team's own step writes and retries
// pin the reserve past the renewals it exists to keep alive.
func (s *Server) allowInflightReserve(w http.ResponseWriter, team string, now time.Time) bool {
	t := s.tokenBudget
	allowed, wait := t.inflight.AllowOrWait("team:"+team, now)
	if allowed {
		return true
	}
	observePrincipalThrottled(budgetClassInflight)
	s.logger.Warn("request shed",
		"team", team, "route_class", budgetClassInflight, "retry_after", wait,
		"reason", "team request budget and in-flight reserve exhausted")
	writeRetryAfter(w, wait, fmt.Sprintf("team request limit is %d per minute and its in-flight reserve of %d per minute for lease renewals and running job results is spent; the runner waits for Retry-After; reduce the team's other requests or ask the operator to raise --requests-per-token-minute", t.policy.PerTokenMinute, inflightReserve(t.policy.PerTokenMinute)))
	return false
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
