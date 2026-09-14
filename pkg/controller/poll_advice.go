package controller

import (
	"net/http"
	"strconv"
	"sync"
	"time"

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

// WithIdleClaimPollEnforced answers a claim that arrives sooner than the idle
// interval this controller last suggested that runner with 429 and a
// Retry-After naming the rest of the wait, instead of a claim. A runner that
// honors the suggestion is never refused, the refusal lifts the moment work
// arrives, and a controller that suggests nothing enforces nothing.
func (s *Server) WithIdleClaimPollEnforced(on bool) *Server {
	if !on {
		s.idlePolls = nil
		return s
	}
	s.idlePolls = &idlePollGate{marks: make(map[string]idlePollMark)}
	return s
}

// safety: a header set after the status line is never sent, so this runs ahead of WriteHeader.
func (s *Server) writeClaimPollAdvice(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	advice := s.claimPollAdvice(now)
	if advice <= 0 {
		return
	}
	w.Header().Set(store.ClaimPollAfterHeader, strconv.Itoa(int(advice.Seconds())))
	if s.idlePolls != nil {
		s.idlePolls.record(s.runnerBudgetKey(r), advice, now)
	}
}

// safety: a suggestion is worth remembering only while a runner could still
// break it, and a runner accepts no suggestion wider than this.
const maxIdlePollMarks = 20000

type idlePollMark struct {
	advised time.Duration
	at      time.Time
}

// safety: the suggestion the controller sends grows with how long it has been
// idle, so what a runner must honor is the one it was last sent rather than
// the one the controller would send now.
type idlePollGate struct {
	mu    sync.Mutex
	marks map[string]idlePollMark
}

func (g *idlePollGate) record(key string, advised time.Duration, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.evictLocked(now)
	g.marks[key] = idlePollMark{advised: advised, at: now}
}

// safety: a mark expires in time but not under a flood of fresh runner
// identities, so a full map drops what expired and then drops everything, and
// the gate refuses less rather than growing without bound.
func (g *idlePollGate) evictLocked(now time.Time) {
	if len(g.marks) < maxIdlePollMarks {
		return
	}
	for k, m := range g.marks {
		if now.Sub(m.at) >= m.advised {
			delete(g.marks, k)
		}
	}
	if len(g.marks) >= maxIdlePollMarks {
		clear(g.marks)
	}
}

func (g *idlePollGate) check(key string, now, awarded time.Time) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	mark, ok := g.marks[key]
	if !ok {
		return 0, false
	}
	// safety: work handed out after the suggestion went out makes it stale, so
	// a fleet is never held off a queue that has since filled.
	if awarded.After(mark.at) {
		delete(g.marks, key)
		return 0, false
	}
	wait := mark.advised - now.Sub(mark.at)
	if wait <= 0 {
		return 0, false
	}
	return wait, true
}

// safety: a refusal is written here, so a caller that gets false must return
// without writing its own answer.
func (s *Server) admitIdleClaimPoll(w http.ResponseWriter, r *http.Request) bool {
	if s.idlePolls == nil {
		return true
	}
	key := s.runnerBudgetKey(r)
	wait, refused := s.idlePolls.check(key, time.Now(), time.Unix(0, s.lastClaimAward.Load()))
	if !refused {
		return true
	}
	observePrincipalThrottled(budgetClassIdlePoll)
	s.logger.Warn("claim shed",
		"runner", key, "route_class", budgetClassIdlePoll, "retry_after", wait,
		"reason", "polled sooner than the suggested idle interval")
	writeRetryAfter(w, wait, "poll again no sooner than the interval this controller suggested")
	return false
}
