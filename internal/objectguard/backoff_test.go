package objectguard_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

func TestDelayCeilingDoublesAndStopsAtMax(t *testing.T) {
	b := objectguard.SetBackoffRandom(
		objectguard.Backoff{Base: 100 * time.Millisecond, Max: time.Second},
		func(n int64) int64 { return n - 1 },
	)
	want := []time.Duration{100, 200, 400, 800, 1000, 1000}
	for attempt, ms := range want {
		got := b.Delay(attempt) + time.Nanosecond
		if got != time.Duration(ms)*time.Millisecond {
			t.Fatalf("attempt %d ceiling is %s, want %dms", attempt, got, ms)
		}
	}
}

func TestDelayIsFullJitterInsideTheCeiling(t *testing.T) {
	b := objectguard.Backoff{Base: 50 * time.Millisecond, Max: 400 * time.Millisecond}
	distinct := map[time.Duration]bool{}
	for range 200 {
		d := b.Delay(2)
		if d < 0 || d >= 200*time.Millisecond {
			t.Fatalf("delay %s falls outside [0, 200ms)", d)
		}
		distinct[d] = true
	}
	if len(distinct) < 2 {
		t.Fatal("every delay was identical, so the backoff carries no jitter")
	}
}

func TestWaitStopsOnContextCancellation(t *testing.T) {
	b := objectguard.Backoff{Base: time.Hour, Max: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Wait(ctx, 3); err == nil {
		t.Fatal("Wait returned nil for a cancelled context")
	}
}
