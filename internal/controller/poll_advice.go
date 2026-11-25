package controller

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// DefaultMaxIdleClaimPoll caps the idle interval suggested by the controller.
const DefaultMaxIdleClaimPoll = 5 * time.Second

// IdleClaimPollJitterFactor is the widest a runner stretches a suggestion when
// it spreads its return. An operator sizing the suggestion against another
// window multiplies by this to get the longest a runner may actually wait.
const IdleClaimPollJitterFactor = 1.25

// LongestHonoredIdlePoll includes runner jitter in the capped poll interval.
func LongestHonoredIdlePoll(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(float64(min(d, controllerwire.MaxHonoredIdleClaimPoll)) * IdleClaimPollJitterFactor)
}

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

// queueActivity keeps the idle clocks behind claim poll advice. Each team
// idles on its own clock, so one team's work never shortens the advice
// another team's runners get.
type queueActivity struct {
	mu sync.Mutex
	// all is activity that concerns every team, such as a controller start.
	all int64
	// any is the latest activity of any team, for a claimant that serves
	// every team's queue.
	any   int64
	teams map[store.Team]int64
}

// safety: a team's admissions and awards finish out of order, so a team clock
// keeps its newest reading rather than the last one written.
func (q *queueActivity) record(team store.Team, at int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if team == "" {
		q.all, q.any = at, at
		return
	}
	q.any = max(q.any, at)
	if q.teams == nil {
		q.teams = map[store.Team]int64{}
	}
	q.teams[team] = max(q.teams[team], at)
}

// prune forgets team clocks that can no longer change advice or enforcement:
// a clock older than the controller-start clock, or idle long enough that
// the advice, read one ceiling in the past, is already at the ceiling. A
// forgotten team falls back to the older controller-start clock, which gives
// the same advice.
func (q *queueActivity) prune(now time.Time, ceiling time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	settled := now.Add(-(idleClaimPollFraction + 1) * ceiling).UnixNano()
	for team, at := range q.teams {
		if ceiling <= 0 || at <= q.all || at <= settled {
			delete(q.teams, team)
		}
	}
}

func (q *queueActivity) last(team store.Team) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if team == "" {
		return q.any
	}
	return max(q.teams[team], q.all)
}

// recordQueueActivity resets every team's idle clock, as a controller start
// does, so no runner is advised to wait on a queue nobody has read yet.
func (s *Server) recordQueueActivity(now time.Time) {
	s.queueActivity.record("", now.UnixNano())
}

// safety: the idle clock measures from here, and work queued or handed out
// for a team resets it, or that team's runners advised while idle would still
// be waiting after its queue filled. Claimants that serve every team reset too.
func (s *Server) recordTeamQueueActivity(team store.Team, now time.Time) {
	s.queueActivity.record(team, now.UnixNano())
}

// recordingTeamQueueActivity resets the target team's idle clock after handle
// commits and answers, for routes that make queued work claimable. A refused
// request resets nothing.
func (s *Server) recordingTeamQueueActivity(handle func(http.ResponseWriter, *http.Request, capability.Authorized) error) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		if err := handle(w, r, a); err != nil {
			return err
		}
		s.recordTeamQueueActivity(store.Team(a.Target.Team), time.Now())
		return nil
	}
}

// claimPollAdvice suggests an idle interval to a claimant of team, or to a
// claimant of every team when team is empty.
func (s *Server) claimPollAdvice(team store.Team, now time.Time) time.Duration {
	if s.idleClaimPoll <= 0 {
		return 0
	}
	last := s.queueActivity.last(team)
	if last == 0 {
		return 0
	}
	idle := now.Sub(time.Unix(0, last))
	advice := idle / idleClaimPollFraction
	// safety: the advice is a floor a runner may only widen to, so it is withheld
	// until it would be worth a runner's while to widen at all.
	if advice < store.MinIdleClaimPoll {
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
func (s *Server) writeClaimPollAdvice(w http.ResponseWriter, team store.Team) {
	advice := s.claimPollAdvice(team, time.Now())
	if advice <= 0 {
		return
	}
	w.Header().Set(controllerwire.ClaimPollAfterHeader, strconv.Itoa(int(advice.Seconds())))
}

// safety: a refusal is written here, so a caller that gets false must return
// without writing its own answer.
func (s *Server) admitIdleClaimPoll(w http.ResponseWriter, r *http.Request, team store.Team, key string) bool {
	if s.idlePolls == nil {
		return true
	}
	now := time.Now()
	// safety: a runner honors the suggestion it was last sent, not the one the
	// controller would send now, so the gate waits until the suggestion a whole
	// interval ago was already the widest one. Until then, and whenever work
	// arrives, this refuses nothing.
	if s.claimPollAdvice(team, now.Add(-s.idleClaimPoll)) < s.idleClaimPoll {
		return true
	}
	if s.idlePolls.Allow(key, now) {
		return true
	}
	// safety: a refused poll is not charged again, so the bucket never owes
	// more than the interval and the wait named here is never shorter than the
	// real refill nor longer than one suggestion.
	wait := s.idleClaimPoll
	observePrincipalThrottled(budgetClassIdlePoll)
	s.logger.Debug("claim shed",
		"runner", key, "route_class", budgetClassIdlePoll, "retry_after", wait,
		"reason", "polled sooner than the suggested idle interval")
	writeRetryAfter(w, wait, "poll again no sooner than the interval this controller suggested")
	return false
}
