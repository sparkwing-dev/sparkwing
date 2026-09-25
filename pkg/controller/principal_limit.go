package controller

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RecommendedHeartbeatsPerMinute is the per-runner heartbeat budget suggested
// for the cadence the shipped runners keep: a node heartbeat runs every 3s and
// a trigger heartbeat every 3s, and ten times that leaves room for retries and
// bursts while still bounding a runner stuck in a tight loop.
//
// It is a recommendation, not a default: a controller budgets nothing until an
// operator names a number. For the claim routes, work the budget from
// [CompliantClaimPollsPerMinute] instead; an awarded claim spends no budget.
const RecommendedHeartbeatsPerMinute = 1200

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
// On the claim routes the identity is the runner's own word, so each new name
// buys a fresh budget. RunnersPerToken caps how many such names one caller may
// hold at once, a name counting until it has gone unused for ten minutes; zero
// means [DefaultRunnersPerToken]. A new name past the cap is answered 429.
type RequestBudget struct {
	ClaimsPerMinute     int
	HeartbeatsPerMinute int
	RunnersPerToken     int
}

// DefaultRunnersPerToken is the number of self-named runners one caller may
// hold at once when [RequestBudget.RunnersPerToken] names none.
const DefaultRunnersPerToken = 64

const runnerNameIdle = 10 * time.Minute

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

	budgetClassRunnerNames = "runner_names"
)

type principalBudget struct {
	policy     RequestBudget
	claims     *ratelimit.Limiter
	heartbeats *ratelimit.Limiter
	names      *runnerNames
}

func newPrincipalBudget(b RequestBudget) *principalBudget {
	if b.RunnersPerToken <= 0 {
		b.RunnersPerToken = DefaultRunnersPerToken
	}
	p := &principalBudget{policy: b, names: &runnerNames{max: b.RunnersPerToken, held: map[string]map[string]time.Time{}}}
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
		key, ok := s.runnerBudgetKey(w, r)
		if !ok {
			return
		}
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
// other runner sharing it, so the budget is keyed on the runner as well. A name
// the caller supplied is admitted only while the caller holds fewer than the
// cap, so varying it cannot multiply the budget without bound; a refusal is
// already written when this reports false.
func (s *Server) runnerBudgetKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	principal := s.floodKey(r, "")
	identity, supplied := runnerIdentity(r)
	if supplied && !s.requestBudget.admitName(principal, identity, time.Now()) {
		limit := s.requestBudget.values().RunnersPerToken
		observePrincipalThrottled(budgetClassRunnerNames)
		s.logger.Warn("request shed",
			"principal", principal, "runner", identity, "route_class", budgetClassRunnerNames,
			"reason", "caller already holds the most runner names it may", "cap", limit)
		writeRetryAfter(w, runnerNameIdle,
			fmt.Sprintf("this caller already holds %d runner names, the most it may; reuse one", limit))
		return "", false
	}
	return principal + "/" + identity, true
}

// safety: a client-supplied name is taken only where the controller can derive
// none, so a runner cannot widen its own budget on any route that names a run,
// a node or an agent. The flag reports a name the caller supplied.
func runnerIdentity(r *http.Request) (string, bool) {
	for _, value := range []string{"nodeID", "name", "id"} {
		if v := r.PathValue(value); v != "" {
			return v, false
		}
	}
	for _, header := range []string{store.ClaimHolderHeader, store.ClaimMembershipHeader, store.RunnerIdentityHeader} {
		if v := r.Header.Get(header); v != "" {
			return v, true
		}
	}
	// safety: a runner too old to name itself shares this bucket with its
	// peers for the length of a rolling upgrade, which is why the budgets
	// are sized per runner rather than per fleet.
	return "unnamed", false
}

type runnerNames struct {
	mu   sync.Mutex
	max  int
	held map[string]map[string]time.Time
}

func (p *principalBudget) admitName(principal, name string, now time.Time) bool {
	if p == nil || p.names == nil {
		return true
	}
	return p.names.admit(principal, name, now)
}

func (n *runnerNames) admit(principal, name string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	names := n.held[principal]
	if names == nil {
		names = map[string]time.Time{}
		n.held[principal] = names
	}
	if _, ok := names[name]; ok {
		names[name] = now
		return true
	}
	for held, at := range names {
		if now.Sub(at) > runnerNameIdle {
			delete(names, held)
		}
	}
	if len(names) >= n.max {
		return false
	}
	names[name] = now
	return true
}

// safety: an award is work this controller chose to hand out, and the loop that
// gets one re-claims at once rather than waiting its poll interval, so charging
// it would bound how fast a runner may execute rather than how fast it may ask.
// Only a claim that came back empty spends the budget.
func (s *Server) claimBudgeted(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limiter := s.requestBudget.limiter(budgetClassClaim)
		if limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		key, ok := s.runnerBudgetKey(w, r)
		if !ok {
			return
		}
		now := time.Now()
		if !limiter.Peek(key, now) {
			// safety: the refusal is charged, so a runner that keeps knocking
			// through an empty budget is told to wait longer each time.
			_, wait := limiter.AllowWithRetry(key, now)
			observePrincipalThrottled(budgetClassClaim)
			s.logger.Warn("request shed",
				"runner", key, "route_class", budgetClassClaim, "retry_after", wait,
				"reason", "per-runner request budget exhausted")
			writeRetryAfter(w, wait, "too many "+budgetClassClaim+" requests from this runner")
			return
		}
		awarded := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(awarded, r)
		if awarded.status != http.StatusOK {
			limiter.Penalize(key, now)
		}
	})
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
