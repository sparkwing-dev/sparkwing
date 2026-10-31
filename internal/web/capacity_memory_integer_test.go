package web

import (
	"math"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestChargeChainMemoryIntegers(t *testing.T) {
	for _, memory := range []int64{9007199254740993, math.MaxInt64} {
		amounts := map[string]int64{}
		for _, step := range chargeChain(store.PipelineProfile{CPUMeasured: true, PrevPeakMemoryBytes: memory}, 4) {
			amounts[step.Step] = step.MemoryBytes
		}
		if amounts["prev_charge"] != memory {
			t.Errorf("memory steps = %v, want carry %d", amounts, memory)
		}
	}
}
