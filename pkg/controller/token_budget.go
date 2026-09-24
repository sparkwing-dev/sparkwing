package controller

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TokenRequestBudget bounds ordinary requests from one authenticated caller
// across the controller's routes, and names the controller-wide
// rate an operator wants to hear about. The per-runner budgets in
// [RequestBudget] size a cooperating runner's loops; this one bounds the token
// itself, which is what a caller varying the runner it claims to be still
// spends from.
//
// Zero in either field leaves that guard off, which is what a controller
// starts with.
type TokenRequestBudget struct {
	// PerTokenMinute caps ordinary requests one token may make per rolling minute.
	// Past it a request is answered 429 with a Retry-After naming the refill
	// delay. The budget is keyed the way the trigger cap is: the token prefix,
	// or the client address for a caller carrying no token.
	PerTokenMinute int

	// AlarmPerMinute is the request rate, counted across every caller, past
	// which the controller logs at warn and counts an alarm. It refuses
	// nothing: it is the operator's notice that one pod is serving more than
	// it was sized for.
	AlarmPerMinute int
}

// WithTokenRequestBudget installs b as the whole-API per-token budget and
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

func (s *Server) tokenBudgeted(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := s.tokenBudget
		if t == nil {
			next.ServeHTTP(w, r)
			return
		}
		now := time.Now()
		if t.alarm != nil && t.alarm.record(now) {
			observeRequestRateAlarm()
			s.logger.Warn("controller request rate above its alarm",
				"requests_per_minute", t.policy.AlarmPerMinute,
				"reason", "one controller is serving more requests a minute than it was sized for")
		}
		if t.requests == nil {
			next.ServeHTTP(w, r)
			return
		}
		if s.ownRunLiveness(r) {
			next.ServeHTTP(w, r)
			return
		}
		key := s.floodKey(r, "")
		allowed, wait := t.requests.AllowWithRetry(key, now)
		if !allowed && !s.ownAgentLivenessHeartbeat(r) {
			observePrincipalThrottled(budgetClassToken)
			s.logger.Warn("request shed",
				"principal", key, "route_class", budgetClassToken, "retry_after", wait,
				"reason", "per-token request budget exhausted")
			writeRetryAfter(w, wait, "too many requests from this token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// safety: The unmetered liveness lane requires a live claim fence; the route handler validates the write.
func (s *Server) ownRunLiveness(r *http.Request) bool {
	if r.Method != http.MethodPost || s.store == nil {
		return false
	}
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p == nil {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" {
		return false
	}
	var runID, nodeID string
	switch {
	case len(parts) == 5 && parts[2] == "runs" && parts[4] == "heartbeat":
		runID = parts[3]
	case len(parts) == 7 && parts[2] == "runs" && parts[4] == "nodes" && parts[6] == "touch":
		runID, nodeID = parts[3], parts[5]
	case len(parts) == 5 && parts[2] == "triggers" && parts[4] == "heartbeat":
		runID = parts[3]
	default:
		return false
	}
	if runID == "" || (len(parts) == 7 && nodeID == "") {
		return false
	}
	if parts[2] == "triggers" && !p.HasScope(ScopeTriggersClaim) {
		return false
	}
	if parts[2] == "runs" && !p.HasScope(ScopeNodesClaim) {
		return false
	}
	nodeClaim, triggerClaim := claimIdentityShape(r)
	if nodeClaim == triggerClaim {
		return false
	}
	if nodeID != "" {
		if nodeClaim {
			fence, err := nodeClaimFenceFromRequest(r)
			if err != nil {
				return false
			}
			held, err := s.store.NodeClaimFenceIsLive(r.Context(), runID, nodeID, fence, time.Now())
			return err == nil && held
		}
	}
	if triggerClaim {
		generation, err := strconv.ParseInt(r.Header.Get(store.TriggerGenerationHeader), 10, 64)
		if err != nil || generation < 1 {
			return false
		}
		held, err := s.store.TriggerClaimFenceIsLive(r.Context(), runID, claimIdentity(r), generation, time.Now())
		return err == nil && held
	}
	if parts[2] == "runs" && nodeID == "" {
		if _, err := nodeClaimFenceFromRequest(r); err != nil {
			return false
		}
		held, err := s.ownsRun(r.Context(), runID, claimIdentity(r))
		return err == nil && held
	}
	return false
}

// safety: an agent treats a lost liveness heartbeat as fatal and takes every
// node it runs down with it, so a token's beat for the agent it enrolled is
// spared. A beat naming any other agent is somebody else's and is budgeted
// like any other request, or the route would be an unbudgeted lane.
func (s *Server) ownAgentLivenessHeartbeat(r *http.Request) bool {
	name, ok := agentLivenessHeartbeatName(r)
	if !ok || s.store == nil {
		return false
	}
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p == nil || p.TokenPrefix == "" {
		return false
	}
	// safety: this is the ownership the heartbeat handler itself enforces
	// through the store, checked after the budget has refused the request.
	enrolled, err := s.store.ExecutorNameForTokenPrefix(r.Context(), p.TokenPrefix)
	if err != nil {
		return false
	}
	return enrolled == name
}

// safety: the budget runs ahead of the mux, which is what sets path values, so
// the agent's name is read off the path here rather than through PathValue.
func agentLivenessHeartbeatName(r *http.Request) (string, bool) {
	const prefix, suffix = "/api/v1/agents/", "/heartbeat"
	if r.Method != http.MethodPost {
		return "", false
	}
	path := r.URL.Path
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	name := path[len(prefix) : len(path)-len(suffix)]
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
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
