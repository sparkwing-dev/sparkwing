package capacity

import (
	"math"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestMemoryChargeIntegers(t *testing.T) {
	for _, memory := range []int64{9007199254740993, math.MaxInt64} {
		profile := &store.PipelineProfile{CPUMeasured: true, PrevPeakMemoryBytes: memory}
		if got := Resolve(profile, 4, ""); got.MemoryBytes != memory {
			t.Errorf("memory = %d, want %d", got.MemoryBytes, memory)
		}
		changed := &store.PipelineProfile{CPUMeasured: true, PlanHash: "old", PeakCores: 1, PeakMemoryBytes: memory}
		if got := Resolve(changed, 4, "new"); got.MemoryBytes != memory {
			t.Errorf("changed plan memory = %d, want %d", got.MemoryBytes, memory)
		}
	}
}
