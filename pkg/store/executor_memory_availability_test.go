package store

import "testing"

func TestExecutorChargeRetainsMemoryWithoutSustainedCPU(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pinCPU      float64
		pinMemory   int64
		floorCPU    float64
		floorMemory int64
		wantCPU     float64
		wantMemory  int64
	}{
		{"measured memory", 0, 0, 0, 0, 1, 512 << 20},
		{"larger floor", 0, 0, 2, 768 << 20, 4, 1536 << 20},
		{"pin", 7, 4096, 2, 768 << 20, 7, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := &PipelineProfile{SampleCount: 3, CPUMeasured: true, PeakCores: 2, PeakMemoryBytes: 512 << 20, PinnedCores: tc.pinCPU, PinnedMemoryBytes: tc.pinMemory, FloorCores: tc.floorCPU, FloorMemoryBytes: tc.floorMemory}
			got := executorNodeChargeFromSnapshot(nil, "sort", profile)
			if got.Cores != tc.wantCPU || got.MemoryBytes != tc.wantMemory {
				t.Fatalf("unknown sustained CPU with measured memory: got %+v; want %g cores and %d bytes", got, tc.wantCPU, tc.wantMemory)
			}
		})
	}
}
