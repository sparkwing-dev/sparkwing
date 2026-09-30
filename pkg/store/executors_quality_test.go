package store

import "testing"

func TestExecutorChargeRequiresMeasuredResourceHistory(t *testing.T) {
	p := &PipelineProfile{SampleCount: 3, PeakCores: 8, SustainedCores: 6, PeakMemoryBytes: 100, PrevPeakCores: 12, PrevSustainedCores: 10, PrevPeakMemoryBytes: 200, FloorCores: 20, FloorMemoryBytes: 300}
	if got := executorNodeChargeFromSnapshot(nil, "node", p); got != (ExecutorResource{Cores: 1}) {
		t.Errorf("unknown history influenced reservation: %+v", got)
	}
	p.PinnedCores, p.PinnedMemoryBytes = 3, 50
	if got := executorNodeChargeFromSnapshot(nil, "node", p); got != (ExecutorResource{Cores: 3, MemoryBytes: 50}) {
		t.Errorf("explicit pin changed: %+v", got)
	}
	for _, cpu := range []float64{0, 2} {
		p = &PipelineProfile{CPUMeasured: true, SampleCount: 3, PeakCores: cpu, PeakMemoryBytes: 100}
		if got := executorNodeChargeFromSnapshot(nil, "node", p); got != (ExecutorResource{Cores: max(cpu, 0.1), MemoryBytes: 100}) {
			t.Errorf("measured control changed: %+v", got)
		}
	}
}

func TestExecutorChargeRetainsMeasuredCarryAndFloor(t *testing.T) {
	p := &PipelineProfile{CPUMeasured: true, PrevPeakCores: 3, PrevSustainedCores: 3, PrevPeakMemoryBytes: 100}
	if got := executorNodeChargeFromSnapshot(nil, "node", p); got != (ExecutorResource{Cores: 3, MemoryBytes: 100}) {
		t.Errorf("measured carry lost: %+v", got)
	}
	p.FloorCores, p.FloorMemoryBytes = 2, 100
	if got := executorNodeChargeFromSnapshot(nil, "node", p); got != (ExecutorResource{Cores: 4, MemoryBytes: 200}) {
		t.Errorf("measured floor lost: %+v", got)
	}
}
