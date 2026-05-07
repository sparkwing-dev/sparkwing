package sparkwing_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

func TestRunsOn_PopulatesLabels(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "train", &buildJob{}).RunsOn("gpu", "arch=arm64")

	labels := n.RunsOnLabels()
	if len(labels) != 2 || labels[0] != "gpu" || labels[1] != "arch=arm64" {
		t.Fatalf("RunsOnLabels: %v", labels)
	}
}

func TestRunsOn_EmptyClears(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "pkg", &buildJob{}).RunsOn("x").RunsOn()
	if got := n.RunsOnLabels(); got != nil {
		t.Fatalf("RunsOnLabels after clear: %v", got)
	}
}

func TestRunsOn_IgnoresEmptyStrings(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "pkg", &buildJob{}).RunsOn("", "gpu", "")
	labels := n.RunsOnLabels()
	if len(labels) != 1 || labels[0] != "gpu" {
		t.Fatalf("RunsOnLabels: %v", labels)
	}
}

func TestRunsOn_ReturnedSliceIsCopy(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "pkg", &buildJob{}).RunsOn("arm64")
	labels := n.RunsOnLabels()
	labels[0] = "mutated"
	if n.RunsOnLabels()[0] != "arm64" {
		t.Fatal("RunsOnLabels returned slice is not a copy")
	}
}

func TestRunsOn_DefaultIsNil(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "pkg", &buildJob{})
	if got := n.RunsOnLabels(); got != nil {
		t.Fatalf("default RunsOnLabels should be nil, got %v", got)
	}
}
