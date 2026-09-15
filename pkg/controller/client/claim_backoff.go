package client

import (
	"sync"
	"time"
)

// MaxClaimBackoff caps one wait a controller may ask a claim or heartbeat loop
// for, so a Retry-After naming an hour does not park a runner for one.
const MaxClaimBackoff = 30 * time.Second

// ShedWarnInterval is how often a loop being shed says so in its log. A shed
// poll is a healthy controller asking for patience, worth one line a window.
const ShedWarnInterval = time.Minute

// UnavailableBackoff reports how long a claim or heartbeat loop should wait
// after err, and whether the controller asked for the wait rather than the
// request failing. A 429 is backpressure exactly as a 503 is, so a loop that
// repolled at its own cadence through one would keep spending the budget it
// just drained. floor is the loop's own cadence, which it never polls faster
// than; the wait is capped at [MaxClaimBackoff] and spread, so a fleet shed
// together does not return together.
func UnavailableBackoff(err error, floor time.Duration) (time.Duration, bool) {
	after, ok := LoadSignal(err)
	if !ok {
		return 0, false
	}
	wait := max(after, floor)
	wait = min(wait, MaxClaimBackoff)
	return wait + retryJitter(wait), true
}

// PollAdvisor is a controller client that remembers the idle poll interval the
// controller last suggested it. [Client] is one.
type PollAdvisor interface {
	PollAdvice() time.Duration
}

// AdvisedPoll reports how long a claim loop should wait before its next poll of
// an empty queue. The controller's suggestion only ever widens the configured
// cadence, so a runner never polls faster than its operator asked for, it is
// capped at [MaxPollAdvice] so a misconfigured controller cannot park a fleet
// past the placement hold, and the spread keeps a fleet advised together from
// returning together.
func AdvisedPoll(configured time.Duration, advisor PollAdvisor) time.Duration {
	if advisor == nil {
		return configured
	}
	advised := min(advisor.PollAdvice(), MaxPollAdvice)
	if advised <= configured {
		return configured
	}
	return advised + retryJitter(advised)
}

// ShedLog rations a log line to one a window, so a loop being shed says so
// once rather than on every poll.
type ShedLog struct {
	mu    sync.Mutex
	every time.Duration
	last  time.Time
	now   func() time.Time
}

// NewShedLog returns a ShedLog that admits one line every window.
func NewShedLog(every time.Duration) *ShedLog {
	return &ShedLog{every: every, now: time.Now}
}

// Due reports whether this window's line has yet to be written.
func (s *ShedLog) Due() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.last.IsZero() && now.Sub(s.last) < s.every {
		return false
	}
	s.last = now
	return true
}
