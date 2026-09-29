package web

import (
	"math"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestChargeChainMemoryIntegers(t *testing.T) {
	for _, tc := range []struct{ previous, floor, wantFloor int64 }{
		{9007199254740993, math.MaxInt64 / 2, math.MaxInt64 - 1},
		{math.MaxInt64, math.MaxInt64, math.MaxInt64},
	} {
		amounts := map[string]int64{}
		for _, step := range chargeChain(store.PipelineProfile{CPUMeasured: true, PrevPeakMemoryBytes: tc.previous, FloorMemoryBytes: tc.floor}, 4) {
			amounts[step.Step] = step.MemoryBytes
		}
		if amounts["prev_charge"] != tc.previous || amounts["floor"] != tc.wantFloor {
			t.Errorf("memory steps = %v, want carry %d and floor %d", amounts, tc.previous, tc.wantFloor)
		}
	}
}
