package controller

import (
	"sync"
	"time"
)

type runnerHeartbeatRegistry struct {
	mu        sync.Mutex
	m         map[presenceKey]time.Time
	lastSweep time.Time
}

func newRunnerHeartbeatRegistry() *runnerHeartbeatRegistry {
	return &runnerHeartbeatRegistry{m: make(map[presenceKey]time.Time)}
}

func (r *runnerHeartbeatRegistry) record(key presenceKey, at time.Time) {
	if r == nil || key.name == "" || key.tokenPrefix == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastSweep.IsZero() || at.Sub(r.lastSweep) >= time.Minute {
		for candidate, seen := range r.m {
			if at.Sub(seen) > runnerHeadroomStale {
				delete(r.m, candidate)
			}
		}
		r.lastSweep = at
	}
	r.m[key] = at
}

func (r *runnerHeartbeatRegistry) lookup(key presenceKey, now time.Time) (time.Time, bool) {
	if r == nil {
		return time.Time{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for candidate, at := range r.m {
		if now.Sub(at) > runnerHeadroomStale {
			delete(r.m, candidate)
		}
	}
	at, ok := r.m[key]
	return at, ok
}
