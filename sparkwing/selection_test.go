package sparkwing_test

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestPrefers_StoresLabelsInDeclarationOrder(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "build", &buildJob{}).
		Prefers("cloud-linux", "local")

	got := n.PrefersLabels()
	want := []string{"cloud-linux", "local"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PrefersLabels = %v, want %v", got, want)
	}
}

func TestPrefers_EmptyClears(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "build", &buildJob{}).
		Prefers("cloud-linux").
		Prefers()
	if got := n.PrefersLabels(); got != nil {
		t.Fatalf("PrefersLabels after clear: %v", got)
	}
}

func TestPrefers_IgnoresEmptyStrings(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "build", &buildJob{}).Prefers("", "cloud-linux", "")
	got := n.PrefersLabels()
	if len(got) != 1 || got[0] != "cloud-linux" {
		t.Fatalf("PrefersLabels: %v", got)
	}
}

func TestPrefers_ReturnedSliceIsCopy(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "build", &buildJob{}).Prefers("a", "b")
	got := n.PrefersLabels()
	got[0] = "mutated"
	if n.PrefersLabels()[0] != "a" {
		t.Fatal("PrefersLabels returned slice is not a copy")
	}
}

func TestWhenRunner_StoresLabels(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "preflight", &buildJob{}).WhenRunner("local")
	got := n.WhenRunnerLabels()
	if len(got) != 1 || got[0] != "local" {
		t.Fatalf("WhenRunnerLabels: %v", got)
	}
}

func TestWhenRunner_EmptyClears(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "preflight", &buildJob{}).
		WhenRunner("local").
		WhenRunner()
	if got := n.WhenRunnerLabels(); got != nil {
		t.Fatalf("WhenRunnerLabels after clear: %v", got)
	}
}

func TestWhenRunner_AcceptsCommaSeparatedTerm(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "preflight", &buildJob{}).WhenRunner("local,cloud-linux")
	got := n.WhenRunnerLabels()
	if len(got) != 1 || got[0] != "local,cloud-linux" {
		t.Fatalf("WhenRunnerLabels: %v (commas should be preserved verbatim; matcher splits at evaluation)", got)
	}
}

func TestGroup_PrefersDelegatesToMembers(t *testing.T) {
	plan := sparkwing.NewPlan()
	a := sparkwing.Job(plan, "a", &buildJob{})
	b := sparkwing.Job(plan, "b", &buildJob{})
	g := sparkwing.GroupJobs(plan, "g", a, b).Prefers("cloud-linux")
	_ = g
	if got := a.PrefersLabels(); len(got) != 1 || got[0] != "cloud-linux" {
		t.Errorf("a.PrefersLabels = %v", got)
	}
	if got := b.PrefersLabels(); len(got) != 1 || got[0] != "cloud-linux" {
		t.Errorf("b.PrefersLabels = %v", got)
	}
}

func TestGroup_WhenRunnerDelegatesToMembers(t *testing.T) {
	plan := sparkwing.NewPlan()
	a := sparkwing.Job(plan, "a", &buildJob{})
	b := sparkwing.Job(plan, "b", &buildJob{})
	g := sparkwing.GroupJobs(plan, "g", a, b).WhenRunner("local")
	_ = g
	if got := a.WhenRunnerLabels(); len(got) != 1 || got[0] != "local" {
		t.Errorf("a.WhenRunnerLabels = %v", got)
	}
	if got := b.WhenRunnerLabels(); len(got) != 1 || got[0] != "local" {
		t.Errorf("b.WhenRunnerLabels = %v", got)
	}
}

func TestRequires_StillTrimsEmpty_AfterCommaWork(t *testing.T) {
	// Regression: the Requires accessor stores the verbatim term
	// (including any commas). Empty entries continue to drop, matching
	// the pre-Prefers behavior.
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Requires("", "linux,macos", "")
	got := n.RequiresLabels()
	if len(got) != 1 || got[0] != "linux,macos" {
		t.Fatalf("RequiresLabels = %v", got)
	}
}
