package store

import (
	"context"
	"errors"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"
)

func TestCronTickUsesStoreClockAndFencesStaleCompletionProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(141, 829))
	for range 32 {
		tenant, now, _ := stateFixture(t)
		op := tenant.s.AsOperator()
		ttl := time.Duration(1 + random.Int64N(int64(time.Minute)))
		ran, err := op.RunCronTickLeased(t.Context(), "same-replica", ttl, func(ctx context.Context) error {
			*now = now.Add(ttl)
			replacement, err := op.RunCronTickLeased(ctx, "same-replica", time.Minute, func(context.Context) error { return nil })
			if err != nil || !replacement {
				t.Fatalf("replacement = %v, %v", replacement, err)
			}
			return nil
		})
		if !ran || !errors.Is(err, ErrLockHeld) {
			t.Fatalf("stale tick = %v, %v", ran, err)
		}
		var stamp []byte
		if err := tenant.s.queryRow(t.Context(), `SELECT value FROM settings WHERE key=?`, cronTickStamp).Scan(&stamp); err != nil {
			t.Fatal(err)
		}
		if string(stamp) != strconv.FormatInt(now.UnixNano(), 10) {
			t.Fatalf("stamp = %s, want store clock %d", stamp, now.UnixNano())
		}
		*now = now.Add(CronTickWindow - time.Nanosecond)
		suppressed, err := op.RunCronTickLeased(t.Context(), "third", time.Minute, func(context.Context) error { t.Fatal("early tick ran"); return nil })
		if err != nil || suppressed {
			t.Fatalf("held window = %v, %v", suppressed, err)
		}
		*now = now.Add(time.Nanosecond)
		next, err := op.RunCronTickLeased(t.Context(), "third", time.Minute, func(context.Context) error { return nil })
		if err != nil || !next {
			t.Fatalf("expired window = %v, %v", next, err)
		}
	}
}

func TestSingletonReleaseCannotClearAnotherFenceProperty(t *testing.T) {
	tenant, now, _ := stateFixture(t)
	op := tenant.s.AsOperator()
	lease, err := op.AcquireSingletonLease(t.Context(), "schedules.tick", "holder", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(517, 889))
	for i := range 64 {
		forged := lease
		switch i % 3 {
		case 0:
			forged.Generation += 1 + random.Int64N(100000)
		case 1:
			forged.Holder += strconv.FormatUint(random.Uint64(), 10)
		case 2:
			forged.Name = "controller.owner"
		}
		if err := op.ReleaseSingletonLease(t.Context(), forged); !errors.Is(err, ErrLockHeld) {
			t.Fatalf("forged release = %v", err)
		}
	}
	*now = lease.ExpiresAt
	next, err := op.AcquireSingletonLease(t.Context(), lease.Name, "next", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := op.ReleaseSingletonLease(t.Context(), lease); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("stale release = %v", err)
	}
	if err := op.ReleaseSingletonLease(t.Context(), next); err != nil {
		t.Fatal(err)
	}
}
