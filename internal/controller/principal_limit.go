package controller

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/internal/store"
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

// RequestBudget bounds per-minute polls and heartbeats for each verified team/authority and holder.
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
// naming the real refill delay, and logged at warn with the principal and the
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

func (s *Server) runnerBudgetKey(w http.ResponseWriter, r *http.Request, a capability.Authorized) (string, bool) {
	principal := credentialBudgetKey(a.Principal)
	identity := a.Binding.Holder
	supplied := false
	if identity == "" {
		identity = r.Header.Get(store.RunnerIdentityHeader)
		// Bound retained header bytes without merging distinct long runner names.
		if len(identity) > maxAuditValue {
			identity = fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
		}
		supplied = identity != ""
	}
	if identity == "" {
		identity = "unnamed"
	}
	if supplied && !s.requestBudget.admitName(principal, identity, time.Now()) {
		limit := s.requestBudget.values().RunnersPerToken
		observePrincipalThrottled(budgetClassRunnerNames)
		s.logger.Warn("request shed", "principal", principal, "route_class", budgetClassRunnerNames, "cap", limit)
		writeRetryAfter(w, runnerNameIdle, fmt.Sprintf("this caller already holds %d runner names, the most it may; reuse one", limit))
		return "", false
	}
	return principal + "/" + identity, true
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

func (s *Server) admitRunnerRequest(w http.ResponseWriter, r *http.Request, a capability.Authorized, class string) (string, bool) {
	key, ok := s.runnerBudgetKey(w, r, a)
	if !ok {
		return "", false
	}
	if class == budgetClassClaim && !s.admitIdleClaimPoll(w, r, key) {
		return "", false
	}
	limiter := s.requestBudget.limiter(class)
	if limiter == nil {
		return key, true
	}
	now := time.Now()
	allowed := limiter.Peek(key, now)
	var wait time.Duration
	if class != budgetClassClaim {
		allowed, wait = limiter.AllowWithRetry(key, now)
	}
	if !allowed {
		if class == budgetClassClaim {
			_, wait = limiter.AllowWithRetry(key, now)
		}
		observePrincipalThrottled(class)
		s.logger.Warn("request shed", "principal", credentialBudgetKey(a.Principal), "route_class", class, "retry_after", wait)
		writeRetryAfter(w, wait, "too many "+class+" requests from this runner")
	}
	return key, allowed
}

func credentialBudgetKey(p capability.Principal) string {
	if p.Capability.Team != "" && p.Capability.Team != string(store.DefaultTeam) {
		return "team:" + p.Capability.Team
	}
	return "authority:" + string(p.Authority.Kind) + ":" + p.Authority.ID
}

func (s *Server) budgetEndpoint(endpoint Endpoint) Endpoint {
	if endpoint.Authentication != capability.AuthCapability {
		return endpoint
	}
	next := endpoint.Handle
	endpoint.Handle = func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		if !s.allowTokenRequest(w, r, credentialBudgetKey(a.Principal)) {
			return nil
		}
		switch endpoint.Pattern {
		case "/api/v1/runs/{run}/heartbeat", "/api/v1/runs/{run}/jobs/{job}/heartbeat", "/api/v1/claims/{claim}/heartbeat":
			if _, ok := s.admitRunnerRequest(w, r, a, budgetClassBeat); !ok {
				return nil
			}
		}
		return next(w, r, a)
	}
	return endpoint
}
