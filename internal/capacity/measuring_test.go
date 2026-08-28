package capacity

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestResolve_PlanHashChangeReMeasuresAtPriorPeak(t *testing.T) {
	prof := &store.PipelineProfile{
		PlanHash:    "shapeA",
		PeakCores:   4,
		SampleCount: MinSamples + 2,
		CPUMeasured: true,
	}
	got := Resolve(nil, prof, 32, "shapeB")
	if got.Source != store.CostSourceMeasuring {
		t.Fatalf("Source = %q, want measuring", got.Source)
	}
	if got.Cores != WarmStartMultiple*4 {
		t.Errorf("Cores = %v, want %v (prior peak)", got.Cores, WarmStartMultiple*4)
	}
}

func TestResolve_SameHashGraduatedUsesMeasuredPeak(t *testing.T) {
	prof := &store.PipelineProfile{
		PlanHash:    "shapeA",
		PeakCores:   4,
		SampleCount: MinSamples,
		CPUMeasured: true,
	}
	got := Resolve(nil, prof, 32, "shapeA")
	if got.Source != store.CostSourceMeasured {
		t.Fatalf("Source = %q, want measured", got.Source)
	}
	if got.Cores != 4 {
		t.Errorf("Cores = %v, want 4 (measured peak)", got.Cores)
	}
}

func TestResolve_ContendedFloorChargesTwiceFloor(t *testing.T) {
	prof := &store.PipelineProfile{
		PlanHash:    "shapeA",
		FloorCores:  3,
		SampleCount: 1,
		CPUMeasured: true,
	}
	got := Resolve(nil, prof, 32, "shapeA")
	if got.Source != store.CostSourceFloor {
		t.Fatalf("Source = %q, want floor", got.Source)
	}
	if got.Cores != SafetyMultiple*3 {
		t.Errorf("Cores = %v, want %v (2x floor)", got.Cores, SafetyMultiple*3)
	}
}

func TestResolve_FloorOutranksPredecessorWarmStart(t *testing.T) {
	prof := &store.PipelineProfile{
		PlanHash:      "shapeB",
		PrevPeakCores: 1,
		FloorCores:    5,
		SampleCount:   1,
		CPUMeasured:   true,
	}
	got := Resolve(nil, prof, 32, "shapeB")
	if got.Source != store.CostSourceFloor {
		t.Fatalf("Source = %q, want floor (floor exceeds warm start)", got.Source)
	}
	if got.Cores != SafetyMultiple*5 {
		t.Errorf("Cores = %v, want %v", got.Cores, SafetyMultiple*5)
	}
}

func TestResolve_NoEvidenceKeepsColdStartDefault(t *testing.T) {
	prof := &store.PipelineProfile{
		PlanHash:    "shapeA",
		SampleCount: 1,
		CPUMeasured: true,
	}
	got := Resolve(nil, prof, 8, "shapeA")
	if got.Source != store.CostSourceDefault {
		t.Fatalf("Source = %q, want default", got.Source)
	}
	if got.Cores != coldStartCores(8) {
		t.Errorf("Cores = %v, want %v (half machine)", got.Cores, coldStartCores(8))
	}
}

func TestResolve_EmptyPlanHashDisablesVersionTracking(t *testing.T) {
	prof := &store.PipelineProfile{
		PlanHash:    "shapeA",
		PeakCores:   4,
		SampleCount: MinSamples,
		CPUMeasured: true,
	}
	got := Resolve(nil, prof, 32, "")
	if got.Source != store.CostSourceMeasured {
		t.Fatalf("Source = %q, want measured", got.Source)
	}
}
