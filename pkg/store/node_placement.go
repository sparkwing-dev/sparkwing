package store

import (
	"context"
	"strings"
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
// recently. Labels are self-asserted, so they steer the soft preference alone
// and never satisfy a hard requirement.
type RunnerPresence struct {
	Name string
	// Labels are the terms the runner asserted for itself on its last claim.
	Labels []string
	// FreeSlots is how many more nodes the runner said it can take. A runner
	// that advertised no capacity reports none, so it holds nothing back: an
	// unknown ceiling is not evidence of room.
	FreeSlots int
}

// ClaimPlacement is the local-first policy applied to one legacy claim. A node
// whose preference the claiming runner does not advertise waits out Hold,
// measured from the node's hold-from time, while another live runner both
// advertises that preference and satisfies the node's hard requirements with a
// slot to spare. After Hold any eligible runner takes it.
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

type placementDecision struct {
	reason string
	hold   bool
	// safety: a preference the plan declared is worth recording per node; one
	// the controller supplies for every node is not.
	nodeOwned bool
}

func (p ClaimPlacement) decide(needs, nodePrefers []string, runner claimLabels, holdFrom *time.Time, now time.Time) placementDecision {
	prefers := p.prefersFor(nodePrefers)
	nodeOwned := len(nodePrefers) > 0
	switch {
	case len(prefers) == 0:
		return placementDecision{reason: PlacementNone}
	case preferenceMet(prefers, runner.soft):
		return placementDecision{reason: PlacementPreferred, nodeOwned: nodeOwned}
	case p.Hold > 0 && holdFrom != nil && now.Before(holdFrom.Add(p.Hold)) && p.someLiveRunnerCanTakeIt(needs, prefers):
		return placementDecision{hold: true, nodeOwned: nodeOwned}
	default:
		return placementDecision{reason: PlacementFallback, nodeOwned: nodeOwned}
	}
}

// safety: a runner that cannot execute the node is no reason to withhold it
// from one that can, so the preference alone never earns a hold.
func (p ClaimPlacement) someLiveRunnerCanTakeIt(needs, prefers []string) bool {
	for _, runner := range p.Live {
		if runner.FreeSlots < 1 {
			continue
		}
		labels := newClaimLabels(runner.Labels)
		if labelsSatisfied(needs, labels.hard) && preferenceMet(prefers, labels.soft) {
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

// safety: a hard requirement and a soft preference read different sets, because
// a runner's self-asserted location speaks only to the preference.
type claimLabels struct {
	hard map[string]struct{}
	soft map[string]struct{}
}

func newClaimLabels(labels []string) claimLabels {
	out := claimLabels{
		hard: make(map[string]struct{}, len(labels)),
		soft: make(map[string]struct{}, len(labels)),
	}
	for _, label := range labels {
		if label == "" {
			continue
		}
		out.soft[label] = struct{}{}
		// safety: a runner asserts its own location, so those terms buy it no
		// hard requirement; the soft preference is the one place they speak.
		if label != "local" && !strings.HasPrefix(label, "location=") {
			out.hard[label] = struct{}{}
		}
	}
	return out
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
