package controller

import (
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Recommended per-runner request budgets for the claim and heartbeat routes,
// per rolling minute, computed from the cadence the shipped runners actually
// use. A pool runner claims every 500ms (120 a minute); a node heartbeat runs
// every 3s (20 a minute per node) and a trigger heartbeat every 3s. Ten times
// that leaves room for retries and bursts while still bounding a runner stuck
// in a tight loop. An enrolled agent's offer slots poll under the agent's one
// name, so an operator running many slots per agent scales the claim budget by
// that count.
//
// They are recommendations, not defaults: a controller budgets nothing until
// an operator names a number.
const (
	RecommendedClaimsPerMinute     = 1200
	RecommendedHeartbeatsPerMinute = 1200
)

// RecommendedClaimsPerMinuteForSlots returns the claim budget suggested for a
// runner working slots offer slots at once. An enrolled agent's slots all poll
// under the agent's one name, and each slot spends a preparation and an offer
// per round at the 500ms cadence, so its budget is the per-runner
// recommendation multiplied by its max_concurrent. Fewer than one slot is read
// as one.
func RecommendedClaimsPerMinuteForSlots(slots int) int {
	return RecommendedClaimsPerMinute * max(slots, 1)
}

// RequestBudget is how many requests one runner may make per rolling minute to
// the claim routes and to the heartbeat routes. Zero in either field leaves
// that class unlimited, which is what a controller starts with.
//
// The budget is keyed on the runner behind the request: the token prefix
// together with the run, node or agent the route names, and on the two claim
// routes that name none, the identity the runner sends in
// [store.RunnerIdentityHeader]. A fleet sharing one token is therefore
// budgeted runner by runner rather than as one caller. The agent liveness
// heartbeat is never budgeted: losing it takes an agent and every node it runs
// down with it.
//
// This bounds a cooperating runner. On the two claim routes the identity is
// the runner's own word, so a holder of a valid token that varies it gets a
// fresh budget each time; the budget is a guard against a runaway loop, not
// against a caller who already authenticated and means harm.
type RequestBudget struct {
	ClaimsPerMinute     int
	HeartbeatsPerMinute int
}

// WithRequestBudget installs b as the per-runner budget on the claim and
// heartbeat routes. A refused request is answered 429 with a Retry-After
// naming the real refill delay, and logged at warn with the runner and the
// route class.
func (s *Server) WithRequestBudget(b RequestBudget) *Server {
	s.requestBudget = newPrincipalBudget(b)
	return s
}

const (
	budgetWindow        = time.Minute
	budgetClassClaim    = "claim"
	budgetClassBeat     = "heartbeat"
	budgetClassIdlePoll = "idle_poll"
)

type principalBudget struct {
	policy     RequestBudget
	claims     *ratelimit.Limiter
	heartbeats *ratelimit.Limiter
}

func newPrincipalBudget(b RequestBudget) *principalBudget {
	p := &principalBudget{policy: b}
	if b.ClaimsPerMinute > 0 {
		p.claims = ratelimit.New(b.ClaimsPerMinute, budgetWindow)
	}
	if b.HeartbeatsPerMinute > 0 {
		p.heartbeats = ratelimit.New(b.HeartbeatsPerMinute, budgetWindow)
	}
	return p
}

// safety: the view reports the budget the operator asked for, which a limiter
// built from it no longer carries once it is spending tokens.
func (p *principalBudget) values() RequestBudget {
	if p == nil {
		return RequestBudget{}
	}
	return p.policy
}

func (p *principalBudget) limiter(class string) *ratelimit.Limiter {
	if p == nil {
		return nil
	}
	if class == budgetClassClaim {
		return p.claims
	}
	return p.heartbeats
}

func (s *Server) budgeted(class string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limiter := s.requestBudget.limiter(class)
		if limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		key := s.runnerBudgetKey(r)
		allowed, wait := limiter.AllowWithRetry(key, time.Now())
		if !allowed {
			observePrincipalThrottled(class)
			s.logger.Warn("request shed",
				"runner", key, "route_class", class, "retry_after", wait,
				"reason", "per-runner request budget exhausted")
			writeRetryAfter(w, wait, "too many "+class+" requests from this runner")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// safety: budgeting a bare token would let one runner's loop starve every
// other runner sharing it, so the budget is keyed on the runner as well.
func (s *Server) runnerBudgetKey(r *http.Request) string {
	return s.floodKey(r, "") + "/" + runnerIdentity(r)
}

// safety: a client-supplied name is taken only where the controller can derive
// none, so a runner cannot widen its own budget on any route that names a run,
// a node or an agent.
func runnerIdentity(r *http.Request) string {
	for _, value := range []string{"nodeID", "name", "id"} {
		if v := r.PathValue(value); v != "" {
			return v
		}
	}
	for _, header := range []string{store.ClaimHolderHeader, store.ClaimMembershipHeader, store.RunnerIdentityHeader} {
		if v := r.Header.Get(header); v != "" {
			return v
		}
	}
	// safety: a runner too old to name itself shares this bucket with its
	// peers for the length of a rolling upgrade, which is why the budgets
	// are sized per runner rather than per fleet.
	return "unnamed"
}

func (s *Server) claimBudgeted(next http.Handler) http.Handler {
	return s.budgeted(budgetClassClaim, next)
}

// safety: the gate holds a runner to the interval this controller suggested it,
// so it guards only the two routes that carry the suggestion. A route naming
// the trigger or node it wants is no idle poll, and the preparation half of an
// offer round would otherwise charge one round twice.
func (s *Server) idlePollBudgeted(next http.Handler) http.Handler {
	budgeted := s.claimBudgeted(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.admitIdleClaimPoll(w, r) {
			return
		}
		budgeted.ServeHTTP(w, r)
	})
}

func (s *Server) heartbeatBudgeted(next http.Handler) http.Handler {
	return s.budgeted(budgetClassBeat, next)
}
