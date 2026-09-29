package store

import "testing"

func TestExecutorChargeCarriesMeasuredZeroCPU(t *testing.T) {
	for _, memory := range []int64{0, 4096} {
		profile := &PipelineProfile{PrevSustainedCores: new(float64(0)), PrevPeakCores: 6, PrevPeakMemoryBytes: memory}
		got := executorNodeChargeFromSnapshot(nil, "sort", profile)
		if got.Cores != 0.1 || got.MemoryBytes != memory {
			t.Errorf("carried zero CPU with memory %d: got %+v, want 0.1 cores and unchanged memory", memory, got)
		}
	}
}
