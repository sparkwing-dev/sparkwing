package controller

import (
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

// Default per-principal request budgets for the claim and heartbeat routes,
// per rolling minute. A fleet of fifty runners claiming once a second spends
// 3000 claims a minute and heartbeating every five seconds spends 600, so a
// healthy fleet of that size stays well inside both. They bound what one
// misbehaving or looping runner can cost the controller, not what a working
// one needs.
const (
	DefaultClaimsPerMinute     = 12000
	DefaultHeartbeatsPerMinute = 6000
)

// RequestBudget is how many requests one principal may make per rolling minute
// to the claim routes and to the heartbeat routes. Zero in either field leaves
// that class unlimited.
type RequestBudget struct {
	ClaimsPerMinute     int
	HeartbeatsPerMinute int
}

// DefaultRequestBudget returns the budget a controller starts with.
func DefaultRequestBudget() RequestBudget {
	return RequestBudget{
		ClaimsPerMinute:     DefaultClaimsPerMinute,
		HeartbeatsPerMinute: DefaultHeartbeatsPerMinute,
	}
}

// WithRequestBudget installs b as the per-principal budget on the claim and
// heartbeat routes. A refused request is answered 429 with Retry-After and
// logged at warn with the principal and the route class.
func (s *Server) WithRequestBudget(b RequestBudget) *Server {
	s.requestBudget = newPrincipalBudget(b)
	return s
}

const (
	budgetWindow     = time.Minute
	budgetRetryAfter = time.Second
	budgetClassClaim = "claim"
	budgetClassBeat  = "heartbeat"
)

type principalBudget struct {
	claims     *ratelimit.Limiter
	heartbeats *ratelimit.Limiter
}

func newPrincipalBudget(b RequestBudget) *principalBudget {
	p := &principalBudget{}
	if b.ClaimsPerMinute > 0 {
		p.claims = ratelimit.New(b.ClaimsPerMinute, budgetWindow)
	}
	if b.HeartbeatsPerMinute > 0 {
		p.heartbeats = ratelimit.New(b.HeartbeatsPerMinute, budgetWindow)
	}
	return p
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
		key := s.floodKey(r, "")
		if !limiter.Allow(key, time.Now()) {
			observePrincipalThrottled(class)
			s.logger.Warn("request shed",
				"principal", key, "route_class", class,
				"reason", "per-principal request budget exhausted")
			writeRetryAfter(w, budgetRetryAfter, "too many "+class+" requests from this principal")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) claimBudgeted(next http.Handler) http.Handler {
	return s.budgeted(budgetClassClaim, next)
}

func (s *Server) heartbeatBudgeted(next http.Handler) http.Handler {
	return s.budgeted(budgetClassBeat, next)
}
