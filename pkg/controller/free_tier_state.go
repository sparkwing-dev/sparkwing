package controller

import (
	"context"
	"fmt"
)

// FreeTierState is how much of the deployment's free storage is left to hand
// out: open, throttled, or closed once it is spent.
type FreeTierState string

// The free-tier states. FreeTierUnreadable is what the gate reports when the
// source failed; it waitlists new accounts as closed does.
const (
	FreeTierOpen       FreeTierState = "open"
	FreeTierThrottled  FreeTierState = "throttled"
	FreeTierClosed     FreeTierState = "closed"
	FreeTierUnreadable FreeTierState = "unreadable"
)

// SignUpFreeTier reports the free tier closed once every free-team slot is
// taken, and open while one is left. A slot count it cannot read is an
// error, never open, so a sign-up gate reading it fails closed.
func (s *Server) SignUpFreeTier(ctx context.Context) (FreeTierState, error) {
	taken, limit, err := s.store.FreeSlots(ctx)
	if err != nil {
		return FreeTierUnreadable, fmt.Errorf("read free-team slots: %w", err)
	}
	if taken >= limit {
		return FreeTierClosed, nil
	}
	return FreeTierOpen, nil
}
