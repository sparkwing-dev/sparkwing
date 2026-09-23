package teamblob

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/smithy-go"
)

// ErrSuspended matches every *SuspendedError under errors.Is.
var ErrSuspended = errors.New("teamblob: writes and listings are paused")

// SuspendedError is a write or listing the store refused without sending
// it, because the bucket recently refused this service or kept failing.
type SuspendedError struct {
	Until time.Time
	Cause string
}

func (e *SuspendedError) Error() string {
	return fmt.Sprintf("the object store is paused for writes and listings until %s after %s; "+
		"reads and deletes still go through, and one request probes the store when the pause ends",
		e.Until.UTC().Format(time.RFC3339), e.Cause)
}

func (e *SuspendedError) Unwrap() error { return ErrSuspended }

// The bucket's kill switch answers PUT and LIST with 403 once a request
// alarm trips, and a refused request still bills. A 403 on either class
// pauses both at once and for minutes; any other failure pauses them
// after a few in a row, for seconds growing to minutes. When a pause ends
// one request goes through as a probe: success closes the breaker, and
// failure pauses again for longer. GET, HEAD and DELETE never pause,
// because the kill switch leaves them working and a delete is how the
// bucket gets back under its alarms.
const (
	deniedPauseBase = time.Minute
	deniedPauseMax  = 5 * time.Minute
	failuresToPause = 3
	failedPauseBase = 5 * time.Second
	failedPauseMax  = 2 * time.Minute
)

type breaker struct {
	mu       sync.Mutex
	until    time.Time
	open     bool
	probing  bool
	trips    int
	failures int
	cause    string
}

// BreakerState is the write breaker at a point in time.
type BreakerState struct {
	Open  bool      `json:"open"`
	Until time.Time `json:"until,omitzero"`
	Trips int       `json:"trips"`
	Cause string    `json:"cause,omitempty"`
}

func (b *breaker) allow(now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return nil
	}
	if now.Before(b.until) || b.probing {
		return &SuspendedError{Until: b.until, Cause: b.cause}
	}
	b.probing = true
	return nil
}

// paused reports the pause without claiming the probe, so a caller can
// refuse before spending anything, even the HEAD a write starts with.
func (b *breaker) paused(now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open && (now.Before(b.until) || b.probing) {
		return &SuspendedError{Until: b.until, Cause: b.cause}
	}
	return nil
}

// WritesPaused returns a *SuspendedError while writes are paused, so a
// caller can refuse a write before reading its body.
func (s *Store) WritesPaused() error { return s.breaker.paused(s.now()) }

func (b *breaker) record(ctx context.Context, now time.Time, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case err == nil || isNotFound(err):
		b.open, b.probing, b.trips, b.failures, b.cause, b.until = false, false, 0, 0, "", time.Time{}
		return
	// safety: a caller that gave up is not the store failing, so it neither
	// counts nor closes; it only hands the probe back.
	case ctx.Err() != nil:
		b.probing = false
		return
	case denied(err):
		b.trips++
		b.pause(now, deniedPauseBase, deniedPauseMax, "the bucket refused this service (403)")
	default:
		b.failures++
		if b.failures < failuresToPause && !b.probing {
			return
		}
		b.trips++
		b.pause(now, failedPauseBase, failedPauseMax, fmt.Sprintf("%d failed requests in a row", b.failures))
	}
}

func (b *breaker) pause(now time.Time, base, ceiling time.Duration, cause string) {
	d := base
	for i := 1; i < b.trips && d < ceiling; i++ {
		d *= 2
	}
	b.open = true
	b.probing = false
	b.until = now.Add(min(d, ceiling))
	b.cause = cause
}

func (b *breaker) state() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BreakerState{Open: b.open, Until: b.until, Trips: b.trips, Cause: b.cause}
}

func denied(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDenied", "AllAccessDisabled":
			return true
		}
	}
	var status interface{ HTTPStatusCode() int }
	return errors.As(err, &status) && status.HTTPStatusCode() == 403
}

// guarded sends one PUT-class or LIST request through the breaker.
func (s *Store) guarded(ctx context.Context, send func() error) error {
	if err := s.breaker.allow(s.now()); err != nil {
		return err
	}
	err := send()
	s.breaker.record(ctx, s.now(), err)
	return err
}

// Breaker reports whether writes and listings are paused.
func (s *Store) Breaker() BreakerState { return s.breaker.state() }
