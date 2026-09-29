package capacity

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestResolveRetainsMemoryWithoutSustainedCPU(t *testing.T) {
	for _, tc := range []struct {
		name, hash  string
		pin         *Pin
		floorCPU    float64
		floorMemory int64
		wantCPU     float64
		wantMemory  int64
		wantSource  store.CostSource
	}{
		{"measured memory", "old", nil, 0, 0, 1, 512 << 20, store.CostSourceDefault},
		{"larger floor", "old", nil, 2, 768 << 20, 4, 1536 << 20, store.CostSourceFloor},
		{"pin", "old", &Pin{Cores: 7, MemoryBytes: 4096}, 2, 768 << 20, 7, 4096, store.CostSourcePin},
		{"changed version", "new", nil, 2, 768 << 20, 1, 512 << 20, store.CostSourceDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := &store.PipelineProfile{PlanHash: "old", SampleCount: 3, CPUMeasured: true, PeakCores: 2, PeakMemoryBytes: 512 << 20, FloorCores: tc.floorCPU, FloorMemoryBytes: tc.floorMemory}
			got := Resolve(tc.pin, profile, 1, tc.hash)
			if got.Cores != tc.wantCPU || got.MemoryBytes != tc.wantMemory || got.Source != tc.wantSource {
				t.Fatalf("unknown sustained CPU with measured memory: got %+v; want %g cores and %d bytes from %s", got, tc.wantCPU, tc.wantMemory, tc.wantSource)
			}
		})
	}
}
