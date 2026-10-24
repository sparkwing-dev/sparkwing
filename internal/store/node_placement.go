package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// Placement reasons record the winning preference.
const (
	placementPreferred = "preference"
	placementFallback  = "fallback"
	placementNone      = "none"
)

// PlacementPreference holds later preferences while an eligible runner is live; empty Prefers matches everyone.
type PlacementPreference struct {
	Prefers []string
	Hold    time.Duration
}

// ClaimPlacement supplies ordered preferences and authenticated runner liveness.
type ClaimPlacement struct {
	Preferences []PlacementPreference
	Liveness    time.Duration
}

type claimPlacementKey struct{}

// WithClaimPlacement supplies server placement policy to ClaimUnit and reconciliation.
func WithClaimPlacement(ctx context.Context, placement ClaimPlacement) context.Context {
	return context.WithValue(ctx, claimPlacementKey{}, placement)
}

// claimPlacementFromContext returns the server placement policy.
func claimPlacementFromContext(ctx context.Context) (ClaimPlacement, bool) {
	placement, ok := ctx.Value(claimPlacementKey{}).(ClaimPlacement)
	return placement, ok
}

func (p ClaimPlacement) preferencesFor(nodePrefers []string) []PlacementPreference {
	if len(nodePrefers) == 0 {
		return p.Preferences
	}
	var hold time.Duration
	if len(p.Preferences) > 0 {
		hold = p.Preferences[0].Hold
	}
	return []PlacementPreference{{Prefers: nodePrefers, Hold: hold}, {}}
}

func claimLiveness(ctx context.Context, request runspec.Request) time.Duration {
	policy, _ := claimPlacementFromContext(ctx)
	if policy.Liveness <= 0 && (request.Burst != nil || runAnywhere(request)) {
		return time.Minute
	}
	return policy.Liveness
}
