package store

import (
	"math"
	"testing"
	"time"
)

func TestProfileIntegerPercentilesRetainExactValues(t *testing.T) {
	for _, value := range []int64{0, 1<<53 + 1, math.MaxInt64} {
		samples := []profileSample{{D: value, M: value}}
		got := profileFromWindow(samples)
		annotateResourcePercentiles(&got, samples)
		if got.PeakMemoryBytes != value || got.MemoryP50Bytes != value || got.MemoryP95Bytes != value || got.P50Duration != time.Duration(value) || got.P99Duration != time.Duration(value) {
			t.Errorf("value=%d: peak=%d p50=%d p95=%d duration50=%d duration99=%d", value, got.PeakMemoryBytes, got.MemoryP50Bytes, got.MemoryP95Bytes, got.P50Duration, got.P99Duration)
		}
	}
}
