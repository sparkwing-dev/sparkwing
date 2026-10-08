package orchestrator

import (
	"testing"
	"time"
)

func TestCASBackoffCeilingDoublesAndCaps(t *testing.T) {
	prev := time.Duration(0)
	for attempt := range 12 {
		ceiling := s3CASBackoffStep << attempt
		if ceiling > s3CASBackoffCap || ceiling <= 0 {
			ceiling = s3CASBackoffCap
		}
		for range 200 {
			got := casBackoff(attempt)
			if got < 0 || got >= ceiling {
				t.Fatalf("attempt %d: backoff %s outside [0, %s)", attempt, got, ceiling)
			}
		}
		if ceiling < prev {
			t.Fatalf("attempt %d: ceiling %s shrank from %s", attempt, ceiling, prev)
		}
		prev = ceiling
	}
}

func TestCASBackoffSpreadsContendersAcrossTheWholeWait(t *testing.T) {
	seen := map[time.Duration]int{}
	for range 400 {
		seen[casBackoff(6)]++
	}
	if len(seen) < 50 {
		t.Fatalf("400 draws produced only %d distinct backoffs; contenders never decorrelate", len(seen))
	}
}
