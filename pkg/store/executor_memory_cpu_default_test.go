package store

import "testing"

func TestExecutorMemoryDoesNotSelectUnknownCPU(t *testing.T) {
	for _, tc := range []struct {
		name           string
		previousCPU    *float64
		previousMemory int64
		floorMemory    int64
		wantCPU        float64
		wantMemory     int64
	}{
		{"unknown without memory", nil, 0, 0, 1, 0},
		{"unknown with carried memory", nil, 512 << 20, 0, 1, 512 << 20},
		{"unknown with memory floor", nil, 0, 256 << 20, 1, 512 << 20},
		{"measured zero without memory", new(float64(0)), 0, 0, 0.1, 0},
		{"measured zero with carried memory", new(float64(0)), 512 << 20, 0, 0.1, 512 << 20},
		{"measured zero with memory floor", new(float64(0)), 0, 256 << 20, 0.1, 512 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := &PipelineProfile{
				PrevPeakCores:       2,
				PrevSustainedCores:  tc.previousCPU,
				PrevPeakMemoryBytes: tc.previousMemory,
				FloorMemoryBytes:    tc.floorMemory,
			}
			got := executorNodeChargeFromSnapshot(nil, "sort", profile)
			if got.Cores != tc.wantCPU || got.MemoryBytes != tc.wantMemory {
				t.Fatalf("memory changed CPU availability: got %+v; want %g cores and %d bytes", got, tc.wantCPU, tc.wantMemory)
			}
		})
	}
}
