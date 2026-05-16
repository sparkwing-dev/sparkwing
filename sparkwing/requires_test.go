package sparkwing_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestRequires_PopulatesLabels(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "train", &buildJob{}).Requires("gpu", "arch=arm64")

	labels := n.RequiresLabels()
	if len(labels) != 2 || labels[0] != "gpu" || labels[1] != "arch=arm64" {
		t.Fatalf("RequiresLabels: %v", labels)
	}
}

func TestRequires_EmptyClears(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "pkg", &buildJob{}).Requires("x").Requires()
	if got := n.RequiresLabels(); got != nil {
		t.Fatalf("RequiresLabels after clear: %v", got)
	}
}

func TestRequires_IgnoresEmptyStrings(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "pkg", &buildJob{}).Requires("", "gpu", "")
	labels := n.RequiresLabels()
	if len(labels) != 1 || labels[0] != "gpu" {
		t.Fatalf("RequiresLabels: %v", labels)
	}
}

func TestRequires_ReturnedSliceIsCopy(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "pkg", &buildJob{}).Requires("arm64")
	labels := n.RequiresLabels()
	labels[0] = "mutated"
	if n.RequiresLabels()[0] != "arm64" {
		t.Fatal("RequiresLabels returned slice is not a copy")
	}
}

func TestRequires_DefaultIsNil(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "pkg", &buildJob{})
	if got := n.RequiresLabels(); got != nil {
		t.Fatalf("default RequiresLabels should be nil, got %v", got)
	}
}
