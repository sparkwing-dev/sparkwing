package orchestrator

import (
	"testing"
	"time"
)

func TestCASBackoffSpreadsContendersAcrossTheWholeWait(t *testing.T) {
	for attempt := range 6 {
		base := time.Duration(attempt+1) * s3CASBackoffStep
		if base > s3CASBackoffCap {
			base = s3CASBackoffCap
		}
		seen := map[time.Duration]int{}
		for range 200 {
			got := casBackoff(attempt)
			if got < base || got > base+base/2 {
				t.Fatalf("attempt %d: backoff %s outside [%s, %s]", attempt, got, base, base+base/2)
			}
			seen[got]++
		}
		// Contenders draw independently, so a shared attempt number must
		// not hand every one of them the same wait.
		if len(seen) < 2 {
			t.Fatalf("attempt %d: 200 draws produced one backoff %v; contenders never decorrelate", attempt, seen)
		}
		if len(seen) < 50 {
			t.Errorf("attempt %d: 200 draws produced only %d distinct backoffs", attempt, len(seen))
		}
	}
}
