package objectguard

import (
	"context"
	"testing"
	"time"
)

func TestDelayCeilingDoublesAndStopsAtMax(t *testing.T) {
	b := setBackoffRandom(
		backoff{Base: 100 * time.Millisecond, Max: time.Second},
		func(n int64) int64 { return n - 1 },
	)
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second,
		time.Second,
	}
	for attempt, ceiling := range want {
		got := b.delay(attempt) + time.Nanosecond
		if got != ceiling {
			t.Fatalf("attempt %d ceiling is %s, want %s", attempt, got, ceiling)
		}
	}
}

func TestDelayIsFullJitterInsideTheCeiling(t *testing.T) {
	b := backoff{Base: 50 * time.Millisecond, Max: 400 * time.Millisecond}
	distinct := map[time.Duration]bool{}
	for range 200 {
		d := b.delay(2)
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
	b := backoff{Base: time.Hour, Max: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.wait(ctx, 3); err == nil {
		t.Fatal("Wait returned nil for a cancelled context")
	}
}
