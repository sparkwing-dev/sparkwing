package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// Placement reasons stamped on a node when a claim wins it.
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

// RunnerPresence is one claim-mode runner a controller has heard from
// recently.
type RunnerPresence struct {
	Name string
	// Labels are the terms the runner asserted for itself on its last claim.
	// They steer the soft preference as sent; a hard selector reads them only
	// after [match.SelfAsserted] strips the keys a runner may not assert.
	Labels []string
	// Profile is what the controller knows of the runner apart from its labels:
	// the name its credential grants, its observed platform and capacity, and
	// the repositories it accepts.
	Profile match.Profile
	// FreeSlots is how many more nodes the runner said it can take. A runner
	// that advertised no capacity reports none, so it holds nothing back: an
	// unknown ceiling is not evidence of room.
	FreeSlots int
	// Available is the capacity the runner said it has free now, read only in
	// the dimensions its Profile reports a capacity for.
	Available match.Resources
	// TokenPrefix selects the persisted capability and its current ancestry.
	TokenPrefix string
}

// ClaimPlacement is the local-first policy applied to a job claim. A node
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
	// Liveness bounds the age of persisted runner polls eligible to hold work.
	Liveness time.Duration
	// Live are the other runners the controller has heard from inside its
	// liveness window.
	Live []RunnerPresence
}

type claimPlacementKey struct{}

// WithClaimPlacement supplies server placement policy to ClaimUnit and reconciliation.
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
}
