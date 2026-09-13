package store

import (
	"context"
	"time"
)

// Placement reasons stamped on a node when a legacy claim wins it.
const (
	// PlacementPreferred marks a node taken by a runner that advertised its
	// preference.
	PlacementPreferred = "preference"
	// PlacementFallback marks a node taken by a runner that did not advertise
	// its preference, the hold window having passed.
	PlacementFallback = "fallback"
	// PlacementNone marks a node that carried no preference to honor.
	PlacementNone = "none"
)

// RunnerPresence is one legacy claim-mode runner a controller has heard from
// recently: the labels it asserted for itself and whether its last report left
// it a free slot. Labels are self-asserted, so they steer the soft preference
// alone and never satisfy a hard requirement.
type RunnerPresence struct {
	Name       string
	Labels     []string
	AtCapacity bool
}

// ClaimPlacement is the local-first policy applied to one legacy claim. A node
// whose preference the claiming runner does not advertise waits out Hold,
// measured from the node's ready time, while some other live runner does
// advertise it and has a slot. After Hold any eligible runner takes it.
//
// The zero value holds nothing back, which is the behavior of every claim that
// carries no policy.
type ClaimPlacement struct {
	// DefaultPrefers applies to a node whose plan declares no Prefers.
	DefaultPrefers []string
	// Hold is how long a preferred-but-absent runner gets the node to itself.
	Hold time.Duration
	// Live are the other runners the controller has heard from inside its
	// liveness window.
	Live []RunnerPresence
}

type claimPlacementKey struct{}

// WithClaimPlacement carries a local-first policy into [Store.ClaimNextReadyNode]
// and its variants. A context without one claims first-in-first-out.
func WithClaimPlacement(ctx context.Context, placement ClaimPlacement) context.Context {
	return context.WithValue(ctx, claimPlacementKey{}, placement)
}

// ClaimPlacementFromContext returns the policy [WithClaimPlacement] carried.
func ClaimPlacementFromContext(ctx context.Context) (ClaimPlacement, bool) {
	placement, ok := ctx.Value(claimPlacementKey{}).(ClaimPlacement)
	return placement, ok
}

func (p ClaimPlacement) prefersFor(nodePrefers []string) []string {
	if len(nodePrefers) > 0 {
		return nodePrefers
	}
	return p.DefaultPrefers
}

func (p ClaimPlacement) decide(nodePrefers []string, runner map[string]struct{}, readyAt *time.Time, now time.Time) (reason string, hold bool) {
	prefers := p.prefersFor(nodePrefers)
	switch {
	case len(prefers) == 0:
		return PlacementNone, false
	case preferenceMet(prefers, runner):
		return PlacementPreferred, false
	case p.Hold > 0 && readyAt != nil && now.Before(readyAt.Add(p.Hold)) && p.someLiveRunnerPrefers(prefers):
		return "", true
	default:
		return PlacementFallback, false
	}
}

func (p ClaimPlacement) someLiveRunnerPrefers(prefers []string) bool {
	for _, runner := range p.Live {
		if runner.AtCapacity {
			continue
		}
		have := make(map[string]struct{}, len(runner.Labels))
		for _, label := range runner.Labels {
			if label != "" {
				have[label] = struct{}{}
			}
		}
		if preferenceMet(prefers, have) {
			return true
		}
	}
	return false
}

// safety: one satisfied term is the whole preference, as in the enrolled offer
// round where the first matching term ranks an executor.
func preferenceMet(prefers []string, have map[string]struct{}) bool {
	for _, term := range prefers {
		if term == "" {
			continue
		}
		if labelTermSatisfied(term, have) {
			return true
		}
	}
	return false
}

type nodeKey struct {
	runID  string
	nodeID string
}

type nodePlacementEvent struct {
	HolderID string   `json:"holder_id"`
	Reason   string   `json:"reason"`
	Prefers  []string `json:"prefers,omitempty"`
}
