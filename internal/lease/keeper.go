// Package lease keeps every claimed unit tied to the store's lease clock.
package lease

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// Beat carries cancellation and watch demand on the existing renewal cadence.
type Beat struct {
	// ExpiresAt bounds execution and cleanup independently of requested cancellation.
	ExpiresAt       time.Time `json:"expires_at,omitzero"`
	CancelRequested bool      `json:"cancel_requested"`
	CancelReason    string    `json:"cancel_reason,omitempty"`
	Watched         bool      `json:"watched"`
	WatchedNodes    []string  `json:"watched_jobs,omitempty"`
}

// Keeper renews every unit and its bound concurrency under the shared cadence.
type Keeper struct {
	Diagnostics *Diagnostics
	Identity    Identity
	Renew       func(context.Context) (Beat, error)
	Interval    time.Duration
	Lease       time.Duration
	// Observe receives successful watch updates without changing renewal cadence.
	Observe func(Beat)
	// WaitCancellation observes requested cancellation without renewing authority.
	// It blocks until cancellation or context completion and returns nil for the latter.
	WaitCancellation func(context.Context) *CancelError
	// ExecutionEnded stops execution surfaces outside the SDK tree. It runs once and must return promptly.
	ExecutionEnded func()
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

// perf: a floor keeps a lease bound close to now from turning renewal into a busy loop.
const minInterval = time.Second

// ErrLeaseLost ends execution after lock_held, unauthenticated or forbidden renewal.
var ErrLeaseLost = errors.New("lease authority lost")

// ErrSilent ends execution after a full lease interval without a successful renewal.
var ErrSilent = errors.New("lease renewal silent")

// Run returns the unit context and a stop function that cancels and joins renewal.
func (k Keeper) Run(ctx context.Context) (unit context.Context, stop func()) {
	trace := k.Diagnostics.start(ctx, k.Identity)
	hard, hardCancel := context.WithCancelCause(ExecutionContext(ctx))
	unit, cancel := context.WithCancelCause(context.WithValue(ctx, executionKey{}, hard))
	executionDone := make(chan struct{})
	context.AfterFunc(hard, func() {
		defer close(executionDone)
		trace.emit("execution_end", time.Time{}, time.Time{}, "ended", 0)
		if k.ExecutionEnded != nil {
			k.ExecutionEnded()
		}
	})
	done := make(chan struct{})
	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		if k.WaitCancellation != nil {
			if requested := k.WaitCancellation(unit); requested != nil {
				trace.capture("cancel_requested", time.Time{}, time.Time{}, "", 0, "", "wait")
				cancel(requested)
			}
		}
	}()
	watchDone := make(chan struct{})
	renewed := make(chan leaseUpdate)
	expires := time.Now().Add(k.Lease)
	go func(expires time.Time) {
		defer close(watchDone)
		watch(hard, func(err error) { hardCancel(err); cancel(err) }, renewed, expires, trace)
	}(expires)
	go func() {
		defer close(done)
		defer cancel(context.Canceled)
		every := k.Interval
		for unit.Err() == nil {
			if !time.Now().Before(expires) {
				hardCancel(ErrSilent)
				cancel(ErrSilent)
				return
			}
			sent := time.Now()
			renewal := trace.emit("renewal_start", sent, time.Time{}, "", 0)
			beat, err := k.Renew(unit)
			outcome, code := "success", ""
			if err != nil {
				outcome, code = "error", renewalErrorCode(err)
			}
			trace.capture("renewal_finish", time.Time{}, time.Time{}, outcome, 0, code, "")
			if unit.Err() != nil {
				return
			}
			if !time.Now().Before(expires) {
				hardCancel(ErrSilent)
				cancel(ErrSilent)
				return
			}
			if err == nil {
				expires = sent.Add(k.Lease)
				if !beat.ExpiresAt.IsZero() {
					// safety: restating the decoded wall expiry on the monotonic clock keeps a later
					// wall-clock step from moving it.
					now := time.Now()
					if server := now.Add(beat.ExpiresAt.Sub(now)); server.Before(expires) {
						expires = server
					}
				}
				trace.emit("expiry_selected", time.Time{}, expires, "", 0)
				if beat.CancelRequested {
					// safety: recording before the watcher handoff keeps its reset from contending
					// with this record, and the record still precedes the cancellation it reports.
					trace.capture("cancel_requested", time.Time{}, time.Time{}, "", 0, "", "renewal")
				}
				if !expires.After(time.Now()) {
					hardCancel(ErrSilent)
					cancel(ErrSilent)
					return
				}
				every = min(k.Interval, max(time.Until(expires)/3, minInterval))
			}
			next := time.Now().Add(max(every, retryAfter(err)))
			if err == nil {
				select {
				case renewed <- leaseUpdate{expires: expires, renewal: renewal}:
				case <-hard.Done():
					return
				}
				if beat.CancelRequested {
					reason := beat.CancelReason
					if reason == "" {
						reason = "cancel_requested"
					}
					cancel(&CancelError{Reason: reason})
				}
				if k.Observe != nil {
					k.Observe(beat)
				}
				if beat.CancelRequested {
					return
				}
			}
			var failure *apierr.Error
			if errors.As(err, &failure) && failure != nil && (failure.Code == hosterr.LockHeld || failure.Code == hosterr.Unauthenticated || failure.Code == hosterr.Forbidden) {
				hardCancel(ErrLeaseLost)
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
		hardCancel(context.Canceled)
		<-done
		<-watchDone
		<-cancelDone
		<-executionDone
	}
}

func renewalErrorCode(err error) string {
	// safety: only the wire code is recorded, because an error message can carry run or user text.
	var failure *apierr.Error
	if errors.As(err, &failure) && failure != nil && failure.Code != "" {
		return string(failure.Code)
	}
	return "unclassified"
}

func retryAfter(err error) time.Duration {
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure == nil {
		return 0
	}
	if failure.Code != hosterr.RateLimited && failure.Code != hosterr.QuotaExceeded && failure.Code != hosterr.Unavailable {
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

type leaseUpdate struct {
	expires time.Time
	renewal uint64
}

func watch(ctx context.Context, cancel context.CancelCauseFunc, renewed <-chan leaseUpdate, expires time.Time, trace *keeperTrace) {
	timer := time.NewTimer(time.Until(expires))
	trace.emit("watch_arm", time.Time{}, expires, "", 0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			cancel(ErrSilent)
			return
		case update := <-renewed:
			expires = update.expires
			timer.Reset(time.Until(expires))
			trace.emit("watch_reset", time.Time{}, expires, "", update.renewal)
		}
	}
}
