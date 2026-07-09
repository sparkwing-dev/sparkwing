package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DefaultConcurrencyCacheCap bounds the rows retained in
// concurrency_cache when a maintenance pass evicts by LRU. It matches the
// controller's default cap so a daemonless box and a controller agree on
// how large the cache may grow.
const DefaultConcurrencyCacheCap = 10_000

// metaKeyConcurrencySwept records the wall-clock of the last full
// concurrency maintenance pass so the daemonless run path can throttle the
// global sweep across separate processes.
const metaKeyConcurrencySwept = "concurrency_swept_at"
const metaKeyConcurrencySweepClaim = "concurrency_sweep_claimed_at"
const concurrencySweepClaimTTL = 15 * time.Second

// ConcurrencyMaintenanceResult reports what one janitorial pass over the
// concurrency tables touched. Counts are zero when a pass ran but found
// nothing to do.
type ConcurrencyMaintenanceResult struct {
	Reconciled   int                 `json:"reconciled"`
	StaleHolders []ConcurrencyHolder `json:"stale_holders,omitempty"`
	Promoted     int                 `json:"promoted"`
	CacheExpired int64               `json:"cache_expired"`
	StaleWaiters []ConcurrencyWaiter `json:"stale_waiters,omitempty"`
	CacheEvicted int64               `json:"cache_evicted"`
}

// ConcurrencyMaintenanceOptions tunes a maintenance pass. Zero values fall
// back to package defaults.
type ConcurrencyMaintenanceOptions struct {
	// Lease is the promotion lease handed to waiters reclaimed during the
	// pass. Zero uses DefaultConcurrencyLease.
	Lease time.Duration
	// WaiterMaxAge drops queued waiters older than this. Zero uses twice
	// DefaultConcurrencyLease, lining up with the node-level queue timeout.
	WaiterMaxAge time.Duration
	// CacheCap is the row ceiling for concurrency_cache after LRU eviction.
	// Zero uses DefaultConcurrencyCacheCap.
	CacheCap int
}

func (o *ConcurrencyMaintenanceOptions) withDefaults() {
	if o.Lease <= 0 {
		o.Lease = DefaultConcurrencyLease
	}
	if o.WaiterMaxAge <= 0 {
		o.WaiterMaxAge = 2 * DefaultConcurrencyLease
	}
	if o.CacheCap <= 0 {
		o.CacheCap = DefaultConcurrencyCacheCap
	}
}

// MaintainConcurrency runs the full controller-free janitorial pass over
// the concurrency tables: reconcile keys with idle capacity, reap
// lease-expired holders and promote the next waiters, sweep expired and
// over-cap cache rows, and drop stale or orphaned waiters. It touches only
// finished or expired rows, so it is safe to run alongside live runs and
// idempotent under racing processes hitting the same tables.
//
// Each step runs independently; a failure in one is collected and the
// remaining steps still run, so a single wedged table can't block the rest
// of the sweep. The returned result reports what every step that succeeded
// touched, alongside the joined error.
func (s *Store) MaintainConcurrency(ctx context.Context, opts ConcurrencyMaintenanceOptions) (ConcurrencyMaintenanceResult, error) {
	opts.withDefaults()
	var res ConcurrencyMaintenanceResult
	var errs []error

	if n, err := s.reconcileConcurrencyKeys(ctx, opts.Lease); err != nil {
		errs = append(errs, fmt.Errorf("reconcile keys: %w", err))
	} else {
		res.Reconciled = n
	}

	if stale, err := s.reapStaleConcurrencyHolders(ctx); err != nil {
		errs = append(errs, fmt.Errorf("reap stale holders: %w", err))
	} else {
		res.StaleHolders = stale
		for _, h := range stale {
			if promoted, err := s.PromoteNextWaiters(ctx, h.Key, opts.Lease); err != nil {
				errs = append(errs, fmt.Errorf("promote after reaping holder on %q: %w", h.Key, err))
			} else {
				res.Promoted += len(promoted)
			}
		}
	}

	if n, err := s.sweepExpiredConcurrencyCache(ctx); err != nil {
		errs = append(errs, fmt.Errorf("sweep expired cache: %w", err))
	} else {
		res.CacheExpired = n
	}

	if dropped, err := s.reapStaleConcurrencyWaiters(ctx, opts.WaiterMaxAge); err != nil {
		errs = append(errs, fmt.Errorf("reap stale waiters: %w", err))
	} else {
		res.StaleWaiters = dropped
	}

	if n, err := s.sweepLRUConcurrencyCache(ctx, opts.CacheCap); err != nil {
		errs = append(errs, fmt.Errorf("sweep lru cache: %w", err))
	} else {
		res.CacheEvicted = n
	}

	return res, errors.Join(errs...)
}

// MaintainConcurrencyThrottled runs MaintainConcurrency only when at least
// minInterval has elapsed since the last successful pass recorded in
// sparkwing_meta. A short in-progress claim collapses concurrent daemonless
// starters to one sweep without suppressing retries for the full interval
// after a timeout or failure.
func (s *Store) MaintainConcurrencyThrottled(ctx context.Context, opts ConcurrencyMaintenanceOptions, minInterval time.Duration) (ConcurrencyMaintenanceResult, bool, error) {
	claimTTL := minInterval
	if claimTTL <= 0 || claimTTL > concurrencySweepClaimTTL {
		claimTTL = concurrencySweepClaimTTL
	}
	claimed, claimToken, err := s.claimSweepWindow(ctx, metaKeyConcurrencySwept, metaKeyConcurrencySweepClaim, minInterval, claimTTL)
	if err != nil {
		return ConcurrencyMaintenanceResult{}, false, err
	}
	if !claimed {
		return ConcurrencyMaintenanceResult{}, false, nil
	}
	res, err := s.MaintainConcurrency(ctx, opts)
	if err == nil {
		err = s.stampSweepWindow(ctx, metaKeyConcurrencySwept)
	}
	if cerr := s.clearSweepClaim(ctx, metaKeyConcurrencySweepClaim, claimToken); err == nil {
		err = cerr
	}
	return res, true, err
}
