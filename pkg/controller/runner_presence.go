package controller

import (
	"sort"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: keyed on the credential as well as the name, so one token cannot
// speak for a runner name another token owns.
type presenceKey struct {
	tokenPrefix string
	name        string
}

// safety: a legacy runner polls for a claim only while it has a free slot, so a
// recent poll is both its liveness signal and its offer of capacity.
type runnerPresence struct {
	Labels        []string
	MaxConcurrent int
	ActiveClaims  int
	// safety: a runner that advertised nothing is not the same as one with room,
	// so an absent capacity block is never read as free slots.
	capacityKnown bool
	// safety: counts the nodes handed over since the last poll, so a stale
	// report cannot read as free capacity the runner already spent.
	awarded int
	// safety: a caller that only ever polls has proved nothing, so labels alone
	// cannot reserve the queue for a machine that never executes.
	claimed   bool
	UpdatedAt time.Time
}

func (p runnerPresence) freeSlots() int {
	if !p.capacityKnown || !p.claimed {
		return 0
	}
	return max(p.MaxConcurrent-p.ActiveClaims-p.awarded, 0)
}

type runnerPresenceRegistry struct {
	mu sync.Mutex
	m  map[presenceKey]runnerPresence
}

func newRunnerPresenceRegistry() *runnerPresenceRegistry {
	return &runnerPresenceRegistry{m: map[presenceKey]runnerPresence{}}
}

func (r *runnerPresenceRegistry) record(key presenceKey, labels []string, capacity *claimCapacity, at time.Time) {
	if r == nil || key.name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := runnerPresence{
		Labels: append([]string(nil), labels...), UpdatedAt: at,
		claimed: r.m[key].claimed,
	}
	if capacity != nil {
		p.MaxConcurrent = capacity.MaxConcurrent
		p.ActiveClaims = capacity.ActiveClaims
		p.capacityKnown = true
	}
	r.m[key] = p
}

// safety: an award is both what proves a runner real and what spends one of the
// slots it advertised.
func (r *runnerPresenceRegistry) awarded(key presenceKey) {
	if r == nil || key.name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.m[key]; ok {
		p.awarded++
		p.claimed = true
		r.m[key] = p
	}
}

// safety: the agents view knows a runner by the name segment of its holder id
// and not by the credential behind it, so the newest row under that name wins.
func (r *runnerPresenceRegistry) lookup(name string, now time.Time, within time.Duration) (runnerPresence, bool) {
	if r == nil {
		return runnerPresence{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var newest runnerPresence
	found := false
	for key, p := range r.m {
		if key.name != name || now.Sub(p.UpdatedAt) > within {
			continue
		}
		if !found || p.UpdatedAt.After(newest.UpdatedAt) {
			newest, found = p, true
		}
	}
	return newest, found
}

// safety: forgetting the rows that fell out of the window here is what bounds
// the registry, which otherwise grows with every runner name ever seen.
func (r *runnerPresenceRegistry) live(now time.Time, within time.Duration, exclude presenceKey) []store.RunnerPresence {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]store.RunnerPresence, 0, len(r.m))
	for key, p := range r.m {
		if now.Sub(p.UpdatedAt) > within {
			delete(r.m, key)
			continue
		}
		if key == exclude {
			continue
		}
		out = append(out, store.RunnerPresence{
			Name: key.name, Labels: p.Labels, FreeSlots: p.freeSlots(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// safety: the placement read is what prunes the registry, so a sampler reads
// without deleting: an export must not decide which runners a later claim sees.
func (r *runnerPresenceRegistry) liveLabelSets(now time.Time, within time.Duration) [][]string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, 0, len(r.m))
	for _, p := range r.m {
		if now.Sub(p.UpdatedAt) > within {
			continue
		}
		out = append(out, p.Labels)
	}
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
