package capacity

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestResolve_PlanHashChangeReMeasuresAtTwicePrior: a
// structural change (a new plan hash) prices the changed version at the
// safety multiple of its predecessor's measured peak, not on the stale
// measured peak, and labels the charge measuring.
func TestResolve_PlanHashChangeReMeasuresAtTwicePrior(t *testing.T) {
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
	if got.Cores != SafetyMultiple*4 {
		t.Errorf("Cores = %v, want %v (2x prior peak)", got.Cores, SafetyMultiple*4)
	}
}

// TestResolve_SameHashGraduatedUsesMeasuredPeak confirms a version whose
// plan hash matches its graduated profile is priced on the measured peak,
// not re-measured.
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

// TestResolve_ContendedFloorChargesTwiceFloor: a version still
// short of clean samples but with a demand floor from its contended runs is
// charged the safety multiple of that floor, sourced as floor.
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

// TestResolve_FloorOutranksPredecessorWarmStart confirms the larger of the
// warm-start (2x predecessor) and the floor (2x floor) wins, and the source
// names whichever drove the charge.
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

// TestResolve_NoEvidenceKeepsColdStartDefault confirms a version still
// gathering its first clean samples -- no floor, no predecessor -- stays on
// the cold-start default rather than reading as measuring.
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

// TestResolve_EmptyPlanHashDisablesVersionTracking confirms a caller that
// passes no plan hash (per-node and cluster paths) keeps the plain
// pin-measured-default order: a graduated profile still resolves measured
// even though its stored hash differs.
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
