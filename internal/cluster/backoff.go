package cluster

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

const (
	// safety: a Retry-After naming an hour would otherwise park a runner for one, so the invitation is bounded here.
	maxClaimBackoff = 30 * time.Second

	// ShedWarnInterval is how often a loop being shed says so: a shed poll is
	// a healthy controller asking for patience, worth one line a window.
	ShedWarnInterval = time.Minute

	// safety: a runner silent past the controller's placement hold drops out of
	// local-first placement, so the invitation to poll less often is bounded here too.
	maxAdvisedPoll = 8 * time.Second
)

// safety: a Retry-After the server rounded to nothing would otherwise spin a
// heartbeat loop, so every shed beat waits at least this long.
var minShedBackoff = time.Second

// UnavailableBackoff reports how long a claim or heartbeat loop should wait
// after err, and whether the controller asked for the wait rather than the
// request failing. A 429 is backpressure exactly as a 503 is, so a loop that
// ignored it would keep spending the budget it just drained.
func UnavailableBackoff(err error, floor time.Duration) (time.Duration, bool) {
	after, ok := client.LoadSignal(err)
	if !ok {
		return 0, false
	}
	wait := after
	if wait < floor {
		wait = floor
	}
	if wait > maxClaimBackoff {
		wait = maxClaimBackoff
	}
	return wait + backoffJitter(wait), true
}

// safety: a fleet shed together would return together, so each waits a spread past the invitation.

// #nosec G404 -- retry spread, not a security decision
func backoffJitter(wait time.Duration) time.Duration {
	if wait <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(wait/4) + 1))
}

// PollAdvisor is a controller client that remembers the idle poll interval the
// controller last suggested it.
type PollAdvisor interface {
	PollAdvice() time.Duration
}

// AdvisedPoll reports how long a claim loop should wait before its next poll of
// an empty queue. The controller's suggestion only ever widens the configured
// cadence, so a runner never polls faster than its operator asked for, and the
// spread keeps a fleet advised together from returning together.
func AdvisedPoll(configured time.Duration, advisor PollAdvisor) time.Duration {
	if advisor == nil {
		return configured
	}
	advised := advisor.PollAdvice()
	if advised > maxAdvisedPoll {
		advised = maxAdvisedPoll
	}
	if advised <= configured {
		return configured
	}
	return advised + backoffJitter(advised)
}

// ShedLog rations a log line to one a window.
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
