package cluster

import (
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

const (
	// safety: a Retry-After naming an hour would otherwise park a runner for one, so the invitation is bounded here.
	maxClaimBackoff = 30 * time.Second

	shedWarnInterval = time.Minute

	// safety: a controller naming an implausible interval would park a runner
	// for it, so the invitation to poll less often is bounded here too.
	maxAdvisedPoll = 60 * time.Second
)

func unavailableBackoff(err error, floor time.Duration) (time.Duration, bool) {
	var shed *client.UnavailableError
	if !errors.As(err, &shed) {
		return 0, false
	}
	wait := shed.RetryAfter
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

type pollAdvisor interface {
	PollAdvice() time.Duration
}

// safety: the controller's suggestion only ever widens the configured cadence, so an agent never polls
// faster than its operator asked for, and the spread keeps a fleet advised together from returning together.
func advisedPoll(configured time.Duration, advisor pollAdvisor) time.Duration {
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

type shedLog struct {
	mu    sync.Mutex
	every time.Duration
	last  time.Time
	now   func() time.Time
}

func newShedLog(every time.Duration) *shedLog {
	return &shedLog{every: every, now: time.Now}
}

// safety: a shed poll is a healthy controller asking for patience, worth one line a window.
func (s *shedLog) due() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.last.IsZero() && now.Sub(s.last) < s.every {
		return false
	}
	s.last = now
	return true
}
