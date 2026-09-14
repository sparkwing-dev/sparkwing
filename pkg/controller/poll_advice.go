package controller

import (
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// DefaultMaxIdleClaimPoll is the widest interval a controller suggests to a
// claim loop that keeps finding no work. It sits far enough below the default
// placement hold that a runner honoring it, jitter included, still polls
// inside the window local-first placement measures a runner's liveness in.
const DefaultMaxIdleClaimPoll = 5 * time.Second

// IdleClaimPollJitterFactor is the widest a runner stretches a suggestion when
// it spreads its return. An operator sizing the suggestion against another
// window multiplies by this to get the longest a runner may actually wait.
const IdleClaimPollJitterFactor = 1.25

// MaxHonoredIdleClaimPoll bounds what a runner accepts however long a
// controller suggests, so a misconfigured controller cannot park a fleet past
// the default placement hold. Two of the longest wait it permits, spread
// included, still fit inside that hold.
const MaxHonoredIdleClaimPoll = 8 * time.Second

// LongestHonoredIdlePoll reports how long a runner may actually wait after
// being suggested d, jitter included. Operators and startup checks compare it
// against the placement windows a silent runner falls out of.
func LongestHonoredIdlePoll(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(float64(min(d, MaxHonoredIdleClaimPoll)) * IdleClaimPollJitterFactor)
}

// safety: the advice is a floor a runner may only widen to, so it is withheld
// until it would be worth a runner's while to widen at all.
const minIdleClaimPoll = 2 * time.Second

// perf: an empty queue answers claims for a quarter of the time it has been
// empty, so the first idle minute walks a one-second poller out to the ceiling
// rather than parking it there the moment one poll comes back empty.
const idleClaimPollFraction = 4

// WithIdleClaimPoll caps the poll interval this controller suggests to claim
// loops while it has no work to hand out. The suggestion travels as a response
// header on the claim routes; a runner honors it only to poll less often, so
// an agent that ignores it keeps the cadence it was configured with. Zero
// suggests nothing.
func (s *Server) WithIdleClaimPoll(max time.Duration) *Server {
	s.idleClaimPoll = max
	s.rebuildIdlePollGate()
	return s
}

// safety: the idle clock measures from here, and work arriving resets it as
// surely as work handed out, or a fleet advised while idle would still be
// waiting minutes after the queue filled.
func (s *Server) recordQueueActivity(now time.Time) {
	s.lastClaimAward.Store(now.UnixNano())
}

func (s *Server) claimPollAdvice(now time.Time) time.Duration {
	if s.idleClaimPoll <= 0 {
		return 0
	}
	last := s.lastClaimAward.Load()
	if last == 0 {
		return 0
	}
	idle := now.Sub(time.Unix(0, last))
	advice := idle / idleClaimPollFraction
	if advice < minIdleClaimPoll {
		return 0
	}
	return min(advice.Truncate(time.Second), s.idleClaimPoll)
}

// WithIdleClaimPollEnforced answers a claim poll that arrives while this
// controller is suggesting its widest idle interval, and sooner than that
// interval, with 429 and a Retry-After naming the rest of the wait. A runner
// that honors the suggestion is never refused, the refusal lifts the moment
// work arrives, and a controller that suggests nothing enforces nothing.
func (s *Server) WithIdleClaimPollEnforced(on bool) *Server {
	s.idlePollsEnforced = on
	s.rebuildIdlePollGate()
	return s
}

// safety: the gate admits one poll per suggested interval, so it is rebuilt
// whenever the suggestion changes rather than reading a window that has moved.
func (s *Server) rebuildIdlePollGate() {
	if !s.idlePollsEnforced || s.idleClaimPoll <= 0 {
		s.idlePolls = nil
		return
	}
	s.idlePolls = ratelimit.New(1, s.idleClaimPoll)
}

// safety: a header set after the status line is never sent, so this runs ahead of WriteHeader.
func (s *Server) writeClaimPollAdvice(w http.ResponseWriter) {
	advice := s.claimPollAdvice(time.Now())
	if advice <= 0 {
		return
	}
	w.Header().Set(store.ClaimPollAfterHeader, strconv.Itoa(int(advice.Seconds())))
}

// safety: a refusal is written here, so a caller that gets false must return
// without writing its own answer.
func (s *Server) admitIdleClaimPoll(w http.ResponseWriter, r *http.Request) bool {
	if s.idlePolls == nil {
		return true
	}
	now := time.Now()
	// safety: a runner honors the suggestion it was last sent, not the one the
	// controller would send now, so the gate waits until the suggestion a whole
	// interval ago was already the widest one. Until then, and whenever work
	// arrives, this refuses nothing.
	if s.claimPollAdvice(now.Add(-s.idleClaimPoll)) < s.idleClaimPoll {
		return true
	}
	key := s.runnerBudgetKey(r)
	allowed, wait := s.idlePolls.AllowWithRetry(key, now)
	if allowed {
		return true
	}
	observePrincipalThrottled(budgetClassIdlePoll)
	s.logger.Warn("claim shed",
		"runner", key, "route_class", budgetClassIdlePoll, "retry_after", wait,
		"reason", "polled sooner than the suggested idle interval")
	writeRetryAfter(w, wait, "poll again no sooner than the interval this controller suggested")
	return false
}
