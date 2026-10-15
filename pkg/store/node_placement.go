package store

import (
	"context"
	"time"
)

// Placement reasons record the winning preference.
const (
	PlacementPreferred = "preference"
	PlacementFallback  = "fallback"
	PlacementNone      = "none"
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

// ClaimPlacementFromContext returns the server placement policy.
func ClaimPlacementFromContext(ctx context.Context) (ClaimPlacement, bool) {
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

type placementDecision struct {
	reason string
	hold   bool
}
