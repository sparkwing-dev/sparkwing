package store

import (
	"context"
	"fmt"
	"time"
)

const (
	metaKeyBucketMeasureAt    = "bucket.measure"
	metaKeyBucketMeasureClaim = "bucket.measure.claim"
)

// RunBucketMeasureLeased runs fn under a store-wide lease, so one
// process measures the object store per window however many share the
// store. It reports whether fn ran: false means another holder owns
// this window, or the last leased measurement is younger than window,
// and is the ordinary answer rather than a failure.
//
// Measuring means enumerating the store, which object stores bill per
// thousand keys, so N replicas measuring on their own timers would
// multiply that bill by N. The lease is advisory in the same way the
// cron tick's is: two processes whose clocks disagree can still overlap,
// which costs one extra listing and no correctness, because a
// measurement only replaces a total.
//
// holder names the caller in the claim and ttl is how long the claim
// survives a caller that dies mid-measurement. The claim is released
// even when fn panics or ctx is already done.
func (s *Store) RunBucketMeasureLeased(ctx context.Context, holder string, window, ttl time.Duration, fn func(context.Context) error) (ran bool, err error) {
	return s.runLeased(ctx, "bucket measurement", metaKeyBucketMeasureAt, metaKeyBucketMeasureClaim, holder, window, ttl, fn)
}

const (
	metaKeyStoragePassAt    = "storage.pass"
	metaKeyStoragePassClaim = "storage.pass.claim"
)

// RunStoragePassLeased runs the storage pass under its own store-wide lease,
// the same way [Store.RunBucketMeasureLeased] runs a measurement: one
// replica per window lists the bucket, reconciles every team's count and
// expires old objects.
func (s *Store) RunStoragePassLeased(ctx context.Context, holder string, window, ttl time.Duration, fn func(context.Context) error) (ran bool, err error) {
	return s.runLeased(ctx, "storage pass", metaKeyStoragePassAt, metaKeyStoragePassClaim, holder, window, ttl, fn)
}

func (s *Store) runLeased(ctx context.Context, what, atKey, claimKey, holder string, window, ttl time.Duration, fn func(context.Context) error) (ran bool, err error) {
	if fn == nil {
		return false, fmt.Errorf("run the %s: a function is required", what)
	}
	if window <= 0 {
		return false, fmt.Errorf("run the %s: a positive window is required", what)
	}
	if ttl <= 0 {
		ttl = window
	}
	claimed, token, err := s.claimSweepWindow(ctx, atKey, claimKey, window, ttl)
	if err != nil {
		return false, fmt.Errorf("claim the %s for %s: %w", what, holder, err)
	}
	if !claimed {
		return false, nil
	}
	ran = true
	// safety: the release outlives ctx, because a run cut short by its own
	// deadline still has to hand the window back rather than sit on the
	// claim until the ttl runs out.
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%s for %s panicked: %v", what, holder, p)
		}
		if cerr := s.clearSweepClaim(context.WithoutCancel(ctx), claimKey, token); err == nil {
			err = cerr
		}
	}()
	if err = fn(ctx); err == nil {
		err = s.stampSweepWindow(ctx, atKey)
	}
	return ran, err
}
