// Package lease keeps every claimed unit tied to the store's lease clock.
package lease

import (
	"context"
	"errors"
	"time"
)

// Beat carries cancellation and watch demand on the existing renewal cadence.
type Beat struct {
	CancelRequested bool     `json:"cancel_requested"`
	Watched         bool     `json:"watched"`
	WatchedNodes    []string `json:"watched_jobs,omitempty"`
}

// Keeper is shared by trigger, node, eval, controller-owner and maintenance leases.
// Interval must pass limits.CheckRenewal and normally comes from limits.Renew.
// Renew also renews the unit's bound concurrency holders and, for a run host,
// its self-claimed nodes in one store statement.
type Keeper struct {
	Renew    func(context.Context) (Beat, error)
	Interval time.Duration
	Lease    time.Duration
	// Observe receives successful watch updates without changing renewal cadence.
	Observe func(Beat)
}

// CancelError distinguishes a requested cancellation from lost authority.
type CancelError struct {
	Reason string
}

// Error exposes the cancellation reason without a bearer or renewal payload.
func (e *CancelError) Error() string { panic("unimplemented: c3-protocol-sdk") }

// ErrLeaseLost ends execution after lock_held, unauthenticated or forbidden renewal.
var ErrLeaseLost = errors.New("lease authority lost")

// ErrSilent ends execution after a full lease interval without a successful renewal.
var ErrSilent = errors.New("lease renewal silent")

// Run returns the unit context and a stop function that cancels and joins its loop.
// The first terminal cause wins: *CancelError, ErrLeaseLost or ErrSilent, or the
// parent's cancellation. A 429/503 honors Retry-After without renewing local
// liveness. Other transient errors also cannot extend the last successful beat.
// Every caller must defer stop, including failed startup and rejected reattach.
func (k Keeper) Run(ctx context.Context) (unit context.Context, stop func()) {
	panic("unimplemented: c3-protocol-sdk")
}
