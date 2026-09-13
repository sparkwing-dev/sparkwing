package controller

import (
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// DefaultMaxIdleClaimPoll is the widest interval a controller suggests to a
// claim loop that keeps finding no work.
const DefaultMaxIdleClaimPoll = 15 * time.Second

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

// safety: the idle clock measures from here, so a controller that just handed out work suggests nothing.
func (s *Server) recordClaimAward(now time.Time) {
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

// safety: a header set after the status line is never sent, so this runs ahead of WriteHeader.
func (s *Server) writeClaimPollAdvice(w http.ResponseWriter) {
	advice := s.claimPollAdvice(time.Now())
	if advice <= 0 {
		return
	}
	w.Header().Set(store.ClaimPollAfterHeader, strconv.Itoa(int(advice.Seconds())))
}
