package controller

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
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
	profile   match.Profile
	available match.Resources
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
	// safety: when the controller accepted its first request, in Unix nanoseconds;
	// zero until then. Startup work before serving is not listening.
	since atomic.Int64
}

func newRunnerPresenceRegistry() *runnerPresenceRegistry {
	return &runnerPresenceRegistry{m: map[presenceKey]runnerPresence{}}
}

// safety: starts the registry's clock once, at the first request it could hear.
func (r *runnerPresenceRegistry) listening(now time.Time) {
	if r != nil && r.since.Load() == 0 {
		r.since.CompareAndSwap(0, now.UnixNano())
	}
}

// safety: only a registry that has listened for a whole liveness window can
// say no runner is live, whichever teams have polled it since. Each controller
// replica keeps its own registry, so this holds only for a single replica,
// which the chart enforces.
func (r *runnerPresenceRegistry) complete(now time.Time, within time.Duration) bool {
	if r == nil || within <= 0 {
		return false
	}
	since := r.since.Load()
	return since != 0 && now.Sub(time.Unix(0, since)) >= within
}

func (r *runnerPresenceRegistry) record(key presenceKey, labels []string, capacity *claimCapacity, profile match.Profile, resources *claimResources, at time.Time) {
	if r == nil || key.name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := runnerPresence{
		Labels: append([]string(nil), labels...), UpdatedAt: at,
		claimed: r.m[key].claimed, profile: profile,
	}
	if resources != nil {
		p.available = resources.Availability
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

// safety: a different credential can claim the same display name, so only
// the credential behind the stored claim can refresh its liveness.
func (r *runnerPresenceRegistry) lookup(key presenceKey, now time.Time, within time.Duration) (runnerPresence, bool) {
	if r == nil {
		return runnerPresence{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.m[key]
	if !ok || now.Sub(p.UpdatedAt) > within {
		return runnerPresence{}, false
	}
	return p, true
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
			Name: key.name, Labels: p.Labels, FreeSlots: p.freeSlots(), TokenPrefix: key.tokenPrefix,
			Profile: p.profile, Available: p.available,
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
