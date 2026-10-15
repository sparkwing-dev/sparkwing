// Package limits owns shared bounds so hosts, storage and documentation cannot choose different limits.
package limits

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrNoTime means the parent's remaining budget cannot leave the requested settlement margin.
var ErrNoTime = errors.New("no time remains before parent deadline")

// Child derives a deadline, leaving margin before parent; a zero parent means unbounded.
// A nonpositive want requests the parent's remaining time, and zero parent plus nonpositive want
// returns the zero time. Expired parents and margins consuming all remaining time return ErrNoTime.
// Negative margins are invalid. This helper never judges lease liveness; the store clock does that.
func Child(now, parent time.Time, want, margin time.Duration) (time.Time, error) {
	if margin < 0 {
		return time.Time{}, errors.New("deadline margin must not be negative")
	}
	if parent.IsZero() {
		if want <= 0 {
			return time.Time{}, nil
		}
		return now.Add(want), nil
	}
	end := parent.Add(-margin)
	if !end.After(now) {
		return time.Time{}, ErrNoTime
	}
	if want > 0 && now.Add(want).Before(end) {
		end = now.Add(want)
	}
	return end, nil
}

// ChildContext applies Child to ctx.Deadline, retaining cancellation when the result is unbounded.
func ChildContext(ctx context.Context, want, margin time.Duration) (context.Context, context.CancelFunc, error) {
	parent, _ := ctx.Deadline()
	end, err := Child(time.Now(), parent, want, margin)
	if err != nil {
		return nil, nil, err
	}
	if end.IsZero() {
		child, cancel := context.WithCancel(ctx)
		return child, cancel, nil
	}
	child, cancel := context.WithDeadline(ctx, end)
	return child, cancel, nil
}

// Renew returns ttl/3; callers validate positive TTLs before constructing a lease keeper.
func Renew(ttl time.Duration) time.Duration { return ttl / 3 }

// CheckRenewal refuses nonpositive values or a cadence that cannot renew twice strictly before expiry.
func CheckRenewal(every, ttl time.Duration) error {
	if every <= 0 || ttl <= 0 || every >= ttl || every >= ttl-every {
		return errors.New("lease and renewal interval must be positive, with two renewals strictly before expiry")
	}
	return nil
}

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
func Error(text string) string { return boundText(text, int(ErrorBytes)) }

// Record bounds already-masked UTF-8 summary or annotation text within AnnotationBytes with an elision note.
func Record(text string) string { return boundText(text, int(AnnotationBytes)) }

// Failure refuses an oversized already-masked failure record rather than producing malformed structured bytes.
func Failure(raw []byte) ([]byte, error) {
	if int64(len(raw)) > FailureBytes {
		return nil, fmt.Errorf("failure record exceeds %d bytes", FailureBytes)
	}
	return raw, nil
}

func boundText(text string, bound int) string {
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= bound {
		return text
	}
	const note = "\n... [elided] ...\n"
	head := (bound - len(note)) / 2
	tail := len(text) - (bound - len(note) - head)
	for head > 0 && !utf8.RuneStart(text[head]) {
		head--
	}
	for tail < len(text) && !utf8.RuneStart(text[tail]) {
		tail++
	}
	return text[:head] + note + text[tail:]
}
