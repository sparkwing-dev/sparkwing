package controller

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type runnerHeadroom struct {
	Cores       float64
	MemoryBytes int64
	QueueDepth  int
	UpdatedAt   time.Time
}

type runnerHeadroomRegistry struct {
	mu sync.Mutex
	m  map[string]runnerHeadroom
}

func newRunnerHeadroomRegistry() *runnerHeadroomRegistry {
	return &runnerHeadroomRegistry{m: map[string]runnerHeadroom{}}
}

func (r *runnerHeadroomRegistry) record(name string, h runnerHeadroom) {
	if r == nil || name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[name] = h
}

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

type namedRunnerHeadroom struct {
	Name string
	runnerHeadroom
}

func (r *runnerHeadroomRegistry) list(now time.Time, staleAfter time.Duration) []namedRunnerHeadroom {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]namedRunnerHeadroom, 0, len(r.m))
	for name, h := range r.m {
		if now.Sub(h.UpdatedAt) > staleAfter {
			continue
		}
		out = append(out, namedRunnerHeadroom{Name: name, runnerHeadroom: h})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

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
