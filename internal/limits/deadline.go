// Package limits owns shared bounds so hosts, storage and documentation cannot choose different limits.
package limits

import (
	"context"
	"errors"
	"time"
)

// ErrNoTime means the parent's remaining budget cannot leave the requested settlement margin.
var ErrNoTime = errors.New("no time remains before parent deadline")

// Child derives a deadline, leaving margin before parent; a zero parent means unbounded.
// A nonpositive want requests the parent's remaining time, and zero parent plus nonpositive want
// returns the zero time. Expired parents and margins consuming all remaining time return ErrNoTime.
// Negative margins are invalid. This helper never judges lease liveness; the store clock does that.
func Child(now, parent time.Time, want, margin time.Duration) (time.Time, error) {
	panic("unimplemented: c4-surface-config")
}

// ChildContext applies Child to ctx.Deadline, retaining cancellation when the result is unbounded.
func ChildContext(ctx context.Context, want, margin time.Duration) (context.Context, context.CancelFunc, error) {
	panic("unimplemented: c4-surface-config")
}

// Renew returns ttl/3; callers validate positive TTLs before constructing a lease keeper.
func Renew(ttl time.Duration) time.Duration { panic("unimplemented: c4-surface-config") }

// CheckRenewal refuses nonpositive values or a cadence that cannot renew twice strictly before expiry.
func CheckRenewal(every, ttl time.Duration) error { panic("unimplemented: c4-surface-config") }

// JobDeadline adds JobSlack to attemptDeadline and caps the result at ownerDeadline. A zero attempt
// uses the owner's budget; both zero remain unbounded. An already-expired attempt or owner is ErrNoTime.
// Both the Kubernetes Job and its launch claim use this result, preventing a pod from outliving its owner.
func JobDeadline(now, ownerDeadline, attemptDeadline time.Time) (time.Time, error) {
	for _, bound := range []time.Time{ownerDeadline, attemptDeadline} {
		if !bound.IsZero() && !bound.After(now) {
			return time.Time{}, ErrNoTime
		}
	}
	deadline := ownerDeadline
	if !attemptDeadline.IsZero() {
		attemptEnd := attemptDeadline.Add(JobSlack)
		if deadline.IsZero() || attemptEnd.Before(deadline) {
			deadline = attemptEnd
		}
	}
	return deadline, nil
}

// Error bounds already-masked UTF-8 error text with a head and tail within ErrorBytes.
func Error(text string) string { panic("unimplemented: c4-surface-config") }

// Record bounds already-masked UTF-8 summary or annotation text within AnnotationBytes with an elision note.
func Record(text string) string { panic("unimplemented: c4-surface-config") }

// Failure refuses an oversized already-masked failure record rather than producing malformed structured bytes.
func Failure(raw []byte) ([]byte, error) { panic("unimplemented: c4-surface-config") }
