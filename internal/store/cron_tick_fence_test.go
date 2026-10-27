package store

import (
	"context"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"
)

func TestCronTickStampsStoreClockAfterTheTickProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(141, 829))
	for range 32 {
		tenant, now, _ := stateFixture(t)
		op := tenant.s.AsOperator()
		elapsed := time.Duration(1 + random.Int64N(int64(time.Minute)))
		ran, err := op.RunCronTick(t.Context(), func(ctx context.Context) error {
			*now = now.Add(elapsed)
			return nil
		})
		if !ran || err != nil {
			t.Fatalf("tick = %v, %v", ran, err)
		}
		var stamp []byte
		if err := tenant.s.queryRow(t.Context(), `SELECT value FROM settings WHERE key=?`, cronTickStamp).Scan(&stamp); err != nil {
			t.Fatal(err)
		}
		if string(stamp) != strconv.FormatInt(now.UnixNano(), 10) {
			t.Fatalf("stamp = %s, want store clock %d", stamp, now.UnixNano())
		}
		*now = now.Add(cronTickWindow - time.Nanosecond)
		suppressed, err := op.RunCronTick(t.Context(), func(context.Context) error { t.Fatal("early tick ran"); return nil })
		if err != nil || suppressed {
			t.Fatalf("held window = %v, %v", suppressed, err)
		}
		*now = now.Add(time.Nanosecond)
		next, err := op.RunCronTick(t.Context(), func(context.Context) error { return nil })
		if err != nil || !next {
			t.Fatalf("expired window = %v, %v", next, err)
		}
	}
}
