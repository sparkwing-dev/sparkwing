package jobs

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestAdmissionStressProfilesHaveExpectedShapes(t *testing.T) {
	tests := []struct {
		profile string
		nodes   int
		roots   int
		leaves  int
	}{
		{stressLightSequential, 4, 1, 1},
		{stressLightParallel, 8, 8, 8},
		{stressMediumFanIn, 6, 1, 1},
		{stressCPUHeavy, 6, 6, 6},
		{stressHeavyParallel, 3, 3, 3},
		{stressMatrix, 27, 1, 3},
	}
	for _, tt := range tests {
		t.Run(tt.profile, func(t *testing.T) {
			specs, err := admissionStressProfile(tt.profile)
			if err != nil {
				t.Fatal(err)
			}
			if len(specs) != tt.nodes {
				t.Fatalf("nodes = %d, want %d", len(specs), tt.nodes)
			}
			roots := 0
			for _, spec := range specs {
				if len(spec.Needs) == 0 {
					roots++
				}
			}
			if roots != tt.roots {
				t.Fatalf("roots = %d, want %d", roots, tt.roots)
			}
			if leaves := len(leafSpecIDs(specs)); leaves != tt.leaves {
				t.Fatalf("leaves = %d, want %d", leaves, tt.leaves)
			}
		})
	}
}

func TestAdmissionStressDefaultsToBatchMatrix(t *testing.T) {
	plan := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (AdmissionStress{workload: stressMatrix}).Plan(context.Background(), plan, AdmissionStressArgs{}, sparkwing.RunContext{})
	})
	if got := plan.AdmissionClass; got != sparkwing.AdmissionBatch {
		t.Fatalf("class = %q, want batch", got)
	}
	if got := len(plan.Jobs); got != 27 {
		t.Fatalf("nodes = %d, want 27", got)
	}
}

func TestAdmissionStressRejectsUnknownProfileAndClass(t *testing.T) {
	plan := sparkwing.NewPlan()
	if err := (AdmissionStress{workload: "giant"}).Plan(context.Background(), plan, AdmissionStressArgs{}, sparkwing.RunContext{}); err == nil {
		t.Fatal("unknown workload succeeded, want validation error")
	}
	plan = sparkwing.NewPlan()
	if err := (AdmissionStress{workload: stressMatrix}).Plan(context.Background(), plan, AdmissionStressArgs{Class: "urgent"}, sparkwing.RunContext{}); err == nil {
		t.Fatal("unknown class succeeded, want validation error")
	}
}
