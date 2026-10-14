// Package lease keeps every claimed unit tied to the store's lease clock.
package lease

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
func (e *CancelError) Error() string {
	if e.Reason == "" {
		return "cancellation requested"
	}
	return "cancellation requested: " + e.Reason
}

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
	unit, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	watchDone := make(chan struct{})
	renewed := make(chan time.Time)
	expires := time.Now().Add(k.Lease)
	go func(expires time.Time) {
		defer close(watchDone)
		watch(unit, cancel, renewed, expires)
	}(expires)
	go func() {
		defer close(done)
		defer cancel(context.Canceled)
		for unit.Err() == nil {
			if !time.Now().Before(expires) {
				cancel(ErrSilent)
				return
			}
			beat, err := k.Renew(unit)
			if unit.Err() != nil {
				return
			}
			if !time.Now().Before(expires) {
				cancel(ErrSilent)
				return
			}
			next := time.Now().Add(max(k.Interval, retryAfter(err)))
			if err == nil {
				if beat.CancelRequested {
					cancel(&CancelError{Reason: "cancel_requested"})
				} else {
					expires = time.Now().Add(k.Lease)
					select {
					case renewed <- expires:
					case <-unit.Done():
						return
					}
				}
				if k.Observe != nil {
					k.Observe(beat)
				}
				if beat.CancelRequested {
					return
				}
			}
			var failure *apierr.Error
			if errors.As(err, &failure) && failure != nil && (failure.Code == apierr.LockHeld || failure.Code == apierr.Unauthenticated || failure.Code == apierr.Forbidden) {
				cancel(ErrLeaseLost)
				return
			}
			timer := time.NewTimer(time.Until(next))
			select {
			case <-unit.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return unit, func() {
		cancel(context.Canceled)
		<-done
		<-watchDone
	}
}

func retryAfter(err error) time.Duration {
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure == nil {
		return 0
	}
	if failure.Code != apierr.RateLimited && failure.Code != apierr.QuotaExceeded && failure.Code != apierr.Unavailable {
		return 0
	}
	var milliseconds float64
	switch value := failure.Details["retry_after_ms"].(type) {
	case float64:
		milliseconds = value
	case int:
		milliseconds = float64(value)
	case int64:
		milliseconds = float64(value)
	case json.Number:
		var parseErr error
		milliseconds, parseErr = value.Float64()
		if parseErr != nil {
			return 0
		}
	}
	if !(milliseconds > 0) || math.IsInf(milliseconds, 0) {
		return 0
	}
	if milliseconds >= float64(math.MaxInt64/int64(time.Millisecond)) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(milliseconds * float64(time.Millisecond))
}

func watch(ctx context.Context, cancel context.CancelCauseFunc, renewed <-chan time.Time, expires time.Time) {
	timer := time.NewTimer(time.Until(expires))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			cancel(ErrSilent)
			return
		case expires = <-renewed:
			timer.Reset(time.Until(expires))
		}
	}
}
