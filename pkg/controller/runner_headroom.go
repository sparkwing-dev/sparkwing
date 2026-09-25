package controller

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type runnerHeadroom struct {
	// Team is the team whose credential advertised the headroom; the queue
	// view shows a caller only its own team's runners.
	Team        store.Team
	Cores       float64
	MemoryBytes int64
	QueueDepth  int
	UpdatedAt   time.Time
}

type runnerHeadroomRegistry struct {
	mu sync.Mutex
	m  map[presenceKey]runnerHeadroom
}

func newRunnerHeadroomRegistry() *runnerHeadroomRegistry {
	return &runnerHeadroomRegistry{m: map[presenceKey]runnerHeadroom{}}
}

func (r *runnerHeadroomRegistry) record(key presenceKey, h runnerHeadroom) {
	if r == nil || key.name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[key] = h
}

func (r *runnerHeadroomRegistry) lookup(key presenceKey, now time.Time, staleAfter time.Duration) (runnerHeadroom, bool) {
	if r == nil {
		return runnerHeadroom{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.m[key]
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
	for key, h := range r.m {
		if now.Sub(h.UpdatedAt) > staleAfter {
			continue
		}
		out = append(out, namedRunnerHeadroom{Name: key.name, runnerHeadroom: h})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func holderName(holderID string) (name, kind string) {
	parts := strings.SplitN(holderID, ":", 3)
	if len(parts) < 2 {
		return "", ""
	}
	if len(parts) == 2 && parts[0] != "runner" && parts[0] != "pod" && parts[0] != "agent" &&
		len(parts[1]) >= 16 && strings.IndexFunc(parts[1], func(ch rune) bool { return ch < '0' || ch > '9' }) < 0 {
		return parts[0], "agent"
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
