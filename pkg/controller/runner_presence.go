package controller

import (
	"sort"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a legacy runner polls for a claim only while it has a free slot, so a
// recent poll is both its liveness signal and its offer of capacity.
type runnerPresence struct {
	Labels        []string
	MaxConcurrent int
	ActiveClaims  int
	// safety: counts the nodes handed to this runner since its last poll, which
	// keeps a stale report from reading as free capacity it already spent.
	granted   int
	UpdatedAt time.Time
}

func (p runnerPresence) atCapacity() bool {
	return p.MaxConcurrent > 0 && p.ActiveClaims+p.granted >= p.MaxConcurrent
}

type runnerPresenceRegistry struct {
	mu sync.Mutex
	m  map[string]runnerPresence
}

func newRunnerPresenceRegistry() *runnerPresenceRegistry {
	return &runnerPresenceRegistry{m: map[string]runnerPresence{}}
}

func (r *runnerPresenceRegistry) record(name string, labels []string, capacity *claimCapacity, at time.Time) {
	if r == nil || name == "" {
		return
	}
	p := runnerPresence{Labels: append([]string(nil), labels...), UpdatedAt: at}
	if capacity != nil {
		p.MaxConcurrent = capacity.MaxConcurrent
		p.ActiveClaims = capacity.ActiveClaims
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[name] = p
}

func (r *runnerPresenceRegistry) granted(name string) {
	if r == nil || name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.m[name]; ok {
		p.granted++
		r.m[name] = p
	}
}

func (r *runnerPresenceRegistry) lookup(name string, now time.Time, within time.Duration) (runnerPresence, bool) {
	if r == nil {
		return runnerPresence{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.m[name]
	if !ok || now.Sub(p.UpdatedAt) > within {
		return runnerPresence{}, false
	}
	return p, true
}

// safety: forgetting the rows that fell out of the window here is what bounds
// the registry, which otherwise grows with every runner name ever seen.
func (r *runnerPresenceRegistry) live(now time.Time, within time.Duration, exclude string) []store.RunnerPresence {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]store.RunnerPresence, 0, len(r.m))
	for name, p := range r.m {
		if now.Sub(p.UpdatedAt) > within {
			delete(r.m, name)
			continue
		}
		if name == exclude {
			continue
		}
		out = append(out, store.RunnerPresence{
			Name: name, Labels: p.Labels, AtCapacity: p.atCapacity(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// safety: one runner mints a fresh holder id per claim, so only the name
// segment identifies it across polls, as the agents view groups it.
func presenceName(holderID string) string {
	if name, _ := holderName(holderID); name != "" {
		return name
	}
	return holderID
}
