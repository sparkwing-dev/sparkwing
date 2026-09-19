package jobs

import (
	"context"
	"slices"
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

func TestAdmissionStressPlanPinsResourcesAndClass(t *testing.T) {
	plan := sparkwing.NewPlan()
	err := (AdmissionStress{workload: stressMediumFanIn}).Plan(context.Background(), plan, AdmissionStressArgs{
		Class: string(sparkwing.AdmissionInteractive),
	}, sparkwing.RunContext{})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.AdmissionClassValue(); got != sparkwing.AdmissionInteractive {
		t.Fatalf("class = %q, want interactive", got)
	}
	for _, node := range plan.Nodes() {
		hints := node.ResourceHints()
		if hints == nil || hints.Cores <= 0 || hints.MemoryBytes <= 0 {
			t.Fatalf("%s resources = %#v, want explicit CPU and memory", node.ID(), hints)
		}
	}

	var join *sparkwing.JobNode
	for _, node := range plan.Nodes() {
		if node.ID() == "join" {
			join = node
			break
		}
	}
	if join == nil {
		t.Fatal("join node is missing")
	}
	if got := join.DepIDs(); !slices.Equal(got, []string{"compute-1", "compute-2", "compute-3", "compute-4"}) {
		t.Fatalf("join dependencies = %v", got)
	}
}

func TestAdmissionStressDefaultsToBatchMatrix(t *testing.T) {
	plan := sparkwing.NewPlan()
	if err := (AdmissionStress{workload: stressMatrix}).Plan(context.Background(), plan, AdmissionStressArgs{}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	if got := plan.AdmissionClassValue(); got != sparkwing.AdmissionBatch {
		t.Fatalf("class = %q, want batch", got)
	}
	if got := len(plan.Nodes()); got != 27 {
		t.Fatalf("nodes = %d, want 27", got)
	}
}

func TestAdmissionStressCPUHeavyProfileDoesNotPinMemory(t *testing.T) {
	plan := sparkwing.NewPlan()
	if err := (AdmissionStress{workload: stressCPUHeavy}).Plan(context.Background(), plan, AdmissionStressArgs{}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	for _, node := range plan.Nodes() {
		hints := node.ResourceHints()
		if hints == nil || hints.Cores != 2 || hints.MemoryBytes != 0 {
			t.Fatalf("%s resources = %#v, want 2 CPU cores and unpinned memory", node.ID(), hints)
		}
	}
}

func TestAdmissionStressLightSequentialPinsTheWholeShortRun(t *testing.T) {
	plan := sparkwing.NewPlan()
	if err := (AdmissionStress{workload: stressLightSequential}).Plan(context.Background(), plan, AdmissionStressArgs{}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	hints := plan.ResourceHints()
	if hints == nil || hints.Cores != 0.2 || hints.MemoryBytes != 0 {
		t.Fatalf("plan resources = %#v, want 0.2 cores and unpinned memory", hints)
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
