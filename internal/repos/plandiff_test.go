package repos

import "testing"

func node(id string, deps []string, dec string, steps ...PlanStep) PlanNode {
	return PlanNode{ID: id, Deps: deps, Decision: dec, Steps: steps}
}

func TestDiffPlans_IdenticalIgnoresOrder(t *testing.T) {
	a := Plan{Pipeline: "p", Nodes: []PlanNode{
		node("build", nil, "run", PlanStep{ID: "compile", Decision: "run"}),
		node("test", []string{"build"}, "run"),
	}}
	b := Plan{Pipeline: "p", Nodes: []PlanNode{
		node("test", []string{"build"}, "run"),
		node("build", nil, "run", PlanStep{ID: "compile", Decision: "run"}),
	}}
	d := DiffPlans(a, b)
	if !d.Identical {
		t.Fatalf("reordered identical plans should compare identical: %+v", d)
	}
	if a.Hash() != b.Hash() {
		t.Errorf("hash should ignore node ordering")
	}
}

func TestDiffPlans_DetectsAddedRemovedChanged(t *testing.T) {
	before := Plan{Pipeline: "p", Nodes: []PlanNode{
		node("build", nil, "run"),
		node("test", []string{"build"}, "run"),
		node("lint", nil, "run"),
	}}
	after := Plan{Pipeline: "p", Nodes: []PlanNode{
		node("build", nil, "run"),
		node("test", []string{"build", "lint-fix"}, "run"),
		node("deploy", []string{"test"}, "run"),
	}}
	d := DiffPlans(before, after)
	if d.Identical {
		t.Fatal("plans differ; Identical should be false")
	}
	if len(d.AddedNodes) != 1 || d.AddedNodes[0] != "deploy" {
		t.Errorf("added = %v, want [deploy]", d.AddedNodes)
	}
	if len(d.RemovedNodes) != 1 || d.RemovedNodes[0] != "lint" {
		t.Errorf("removed = %v, want [lint]", d.RemovedNodes)
	}
	if len(d.ChangedNodes) != 1 || d.ChangedNodes[0].ID != "test" {
		t.Fatalf("changed = %v, want [test]", d.ChangedNodes)
	}
}

func TestDiffPlans_DetectsStepAndDecisionChange(t *testing.T) {
	before := Plan{Pipeline: "p", Nodes: []PlanNode{
		node("deploy", nil, "run", PlanStep{ID: "canary", Decision: "run"}),
	}}
	after := Plan{Pipeline: "p", Nodes: []PlanNode{
		node("deploy", nil, "skip", PlanStep{ID: "canary", Decision: "skip"}, PlanStep{ID: "rollout", Decision: "run"}),
	}}
	d := DiffPlans(before, after)
	if len(d.ChangedNodes) != 1 {
		t.Fatalf("want 1 changed node, got %v", d.ChangedNodes)
	}
	details := d.ChangedNodes[0].Details
	joined := ""
	for _, s := range details {
		joined += s + "\n"
	}
	for _, want := range []string{"decision run -> skip", "step +rollout", "step canary decision run -> skip"} {
		if !contains(joined, want) {
			t.Errorf("details missing %q; got:\n%s", want, joined)
		}
	}
}

func contains(hay, needle string) bool {
	return len(needle) == 0 || (len(hay) >= len(needle) && indexOf(hay, needle) >= 0)
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
