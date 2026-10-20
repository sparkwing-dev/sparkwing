package store

import (
	"context"
	"errors"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"
)

func TestBucketMeasurementCannotStampOrReleaseSuccessorProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(332, 598))
	for range 24 {
		tenant, now, _ := stateFixture(t)
		op := tenant.s.AsOperator()
		ttl := time.Duration(random.Int64N(int64(time.Minute))) + 1
		ran, err := op.RunBucketMeasureLeased(t.Context(), "same-replica", time.Hour, ttl, func(ctx context.Context) error {
			*now = now.Add(ttl)
			next, err := op.RunBucketMeasureLeased(ctx, "same-replica", time.Hour, time.Minute, func(context.Context) error { return nil })
			if err != nil || !next {
				t.Fatalf("successor ran=%v: %v", next, err)
			}
			return nil
		})
		if !ran || !errors.Is(err, ErrLockHeld) {
			t.Fatalf("stale measurement ran=%v: %v", ran, err)
		}
		var stamp []byte
		if err := tenant.s.queryRow(t.Context(), `SELECT value FROM settings WHERE key='bucket.measure'`).Scan(&stamp); err != nil {
			t.Fatal(err)
		}
		if string(stamp) != strconv.FormatInt(now.UnixNano(), 10) {
			t.Fatalf("stamp=%s", stamp)
		}
		*now = now.Add(time.Hour - time.Nanosecond)
		if ran, err := op.RunBucketMeasureLeased(t.Context(), "third", time.Hour, time.Minute, func(context.Context) error { t.Fatal("window reopened early"); return nil }); err != nil || ran {
			t.Fatalf("early measurement ran=%v: %v", ran, err)
		}
	}
}
