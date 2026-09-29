package store

import (
	"math"
	"testing"
)

func TestExecutorMemoryIntegers(t *testing.T) {
	for _, tc := range []struct{ floor, want int64 }{
		{math.MaxInt64 / 2, math.MaxInt64 - 1},
		{math.MaxInt64/2 + 1, math.MaxInt64},
		{math.MaxInt64, math.MaxInt64},
	} {
		got := executorNodeChargeFromSnapshot(nil, "node", &PipelineProfile{FloorMemoryBytes: tc.floor})
		if got.MemoryBytes != tc.want {
			t.Errorf("floor %d: memory = %d, want %d", tc.floor, got.MemoryBytes, tc.want)
		}
	}
}
