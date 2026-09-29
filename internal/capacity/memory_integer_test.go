package capacity

import (
	"math"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestMemoryChargeIntegers(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		previous, floor, want int64
	}{
		{"carry", 9007199254740993, 0, 9007199254740993},
		{"maximum carry", math.MaxInt64, 0, math.MaxInt64},
		{"double boundary", 0, math.MaxInt64 / 2, math.MaxInt64 - 1},
		{"double overflow", 0, math.MaxInt64/2 + 1, math.MaxInt64},
		{"maximum floor", 0, math.MaxInt64, math.MaxInt64},
		{"carry wins", 9007199254740993, 2, 9007199254740993},
		{"floor wins", 1, 9007199254740993, 18014398509481986},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := &store.PipelineProfile{PrevPeakMemoryBytes: tc.previous, FloorMemoryBytes: tc.floor}
			for _, resolve := range []func(*Pin, *store.PipelineProfile, int, string) Resolution{Resolve, ResolvePeak} {
				got := resolve(nil, profile, 4, "")
				if got.MemoryBytes != tc.want {
					t.Errorf("memory = %d, want %d", got.MemoryBytes, tc.want)
				}
				if tc.floor == 0 {
					changed := &store.PipelineProfile{PlanHash: "old", PeakCores: 1, PeakMemoryBytes: tc.previous}
					if got := resolve(nil, changed, 4, "new"); got.MemoryBytes != tc.previous {
						t.Errorf("changed plan memory = %d, want %d", got.MemoryBytes, tc.previous)
					}
				}
				if limited := ApplyCeiling(got, 0, 1024); limited.MemoryBytes != 1024 {
					t.Errorf("limited memory = %d, want 1024", limited.MemoryBytes)
				}
			}
		})
	}
}
