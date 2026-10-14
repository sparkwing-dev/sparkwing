package nodemetrics

import (
	"math"
	"testing"
	"time"
)

func TestCPUTicks(t *testing.T) {
	for _, tc := range []struct {
		ticks, frequency uint64
		want             time.Duration
		ok               bool
	}{
		{0, 100, 0, true},
		{150, 100, 1500 * time.Millisecond, true},
		{24_000_000, 24_000_000, time.Second, true},
		{math.MaxInt64, 1_000_000_000, math.MaxInt64, true},
		{uint64(math.MaxInt64) + 1, 1_000_000_000, 0, false},
		{math.MaxUint64, 1, 0, false},
		{1, 0, 0, false},
	} {
		got, ok := cpuTicks(tc.ticks, tc.frequency)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%d ticks at %dHz = %v, %v; want %v, %v", tc.ticks, tc.frequency, got, ok, tc.want, tc.ok)
		}
	}
}
