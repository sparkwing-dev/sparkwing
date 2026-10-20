package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// safety: every lease the store writes, and so every keeper deadline, is the earliest of the default lease, the
// capability's expiry, revocation and binding deadline, and each metered award's funded bound. A lapse settles through the lease,
// so no unit can run on time nobody paid for beyond the one-slice drain a refusal grants.
func leaseBoundTx(ctx context.Context, tx *storeTx, prefix string, now time.Time, funded ...time.Time) (time.Time, error) {
	c, err := loadCapabilityTx(ctx, tx, prefix)
	if err != nil {
		return time.Time{}, err
	}
	return leaseBound(now, append(funded, deref(c.ExpiresAt), deref(c.Binding.Deadline), deref(c.RevokedAt))...)
}

// safety: an absent bound leaves the default lease, and expired authority cannot be renewed.
func leaseBound(now time.Time, bounds ...time.Time) (time.Time, error) {
	expiry := now.Add(limits.DefaultLease)
	for _, bound := range bounds {
		if !bound.IsZero() && bound.Before(expiry) {
			expiry = bound
		}
	}
	if !expiry.After(now) {
		return time.Time{}, holderRefusal()
	}
	return expiry, nil
}

// safety: a funded award is bounded by when its paid seconds run out if it executes from now on, counting the time it
// ran rather than the billing minimum the payment also covers. A refusal can shorten that horizon to its durable
// drain deadline. Work that is not metered has no funded bound.
func fundedBoundTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, generation int64, now time.Time) (time.Time, error) {
	a, err := unitBillingAwardTx(ctx, tx, team, unit, generation)
	if err != nil || a == nil || !a.provenance.Metered {
		return time.Time{}, err
	}
	if a.settled.Valid {
		if a.drain.Valid {
			return time.Unix(0, a.drain.Int64).UTC(), nil
		}
		return time.Time{}, nil
	}
	intervals, err := readBillingIntervalsTx(ctx, tx, *a, now, false)
	if err != nil {
		return time.Time{}, err
	}
	_, paid, err := paidBillingTx(ctx, tx, *a)
	if err != nil {
		return time.Time{}, err
	}
	horizon := now.Add(time.Duration(paid) * time.Second)
	for _, interval := range intervals {
		horizon = horizon.Add(-interval.End.Sub(interval.Start))
	}
	if a.drain.Valid && a.drain.Int64 < horizon.UnixNano() {
		horizon = time.Unix(0, a.drain.Int64).UTC()
	}
	return horizon, nil
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
