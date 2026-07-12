package controller

import (
	"strings"
	"sync"
	"time"
)

// runnerHeadroom is one registered runner's most recently advertised free
// capacity: the local admission daemon's grantable cores and memory after
// the operator's reserve, plus the daemon's queue depth. It is soft state,
// refreshed on every node claim and heartbeat and read back into the
// agents view; it never gates admission (the runner's local daemon is the
// backstop) and does not survive a controller restart.
type runnerHeadroom struct {
	Cores       float64
	MemoryBytes int64
	QueueDepth  int
	UpdatedAt   time.Time
}

// runnerHeadroomRegistry holds the latest advertised headroom per runner,
// keyed by the runner's name (the middle segment of its holder id). All
// methods are safe for concurrent use.
type runnerHeadroomRegistry struct {
	mu sync.Mutex
	m  map[string]runnerHeadroom
}

func newRunnerHeadroomRegistry() *runnerHeadroomRegistry {
	return &runnerHeadroomRegistry{m: map[string]runnerHeadroom{}}
}

// record stores the headroom a runner advertised. An empty name (a holder
// id the agents view cannot attribute) is ignored.
func (r *runnerHeadroomRegistry) record(name string, h runnerHeadroom) {
	if r == nil || name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[name] = h
}

// lookup returns the runner's advertised headroom if it is fresher than
// staleAfter before now. A stale or missing entry returns ok=false so the
// agents view omits headroom rather than showing figures no runner has
// refreshed.
func (r *runnerHeadroomRegistry) lookup(name string, now time.Time, staleAfter time.Duration) (runnerHeadroom, bool) {
	if r == nil {
		return runnerHeadroom{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.m[name]
	if !ok || now.Sub(h.UpdatedAt) > staleAfter {
		return runnerHeadroom{}, false
	}
	return h, true
}

// holderName extracts the runner name and kind from a claim holder id of
// the form "<kind>:<name>[:<suffix>]" (e.g. "runner:host-7:1699..."). It
// mirrors the parsing the agents view uses so advertised headroom keys the
// same runner its active jobs do. A malformed id yields empty strings.
func holderName(holderID string) (name, kind string) {
	parts := strings.SplitN(holderID, ":", 3)
	if len(parts) < 2 {
		return "", ""
	}
	switch parts[0] {
	case "runner":
		kind = "agent"
	case "pod":
		kind = "pool"
	default:
		kind = parts[0]
	}
	return parts[1], kind
}
