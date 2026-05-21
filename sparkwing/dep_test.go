package sparkwing_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestNodeIDOf_PanicsOnEmpty(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on NodeIDOf(\"\")")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "NodeIDOf") || !strings.Contains(msg, "empty") {
			t.Fatalf("panic should explain why; got %q", msg)
		}
	}()
	_ = sparkwing.NodeIDOf("")
}

func TestNodeIDOf_HappyPath(t *testing.T) {
	id := sparkwing.NodeIDOf("upstream")
	if string(id) != "upstream" {
		t.Fatalf("NodeIDOf returned %q, want upstream", id)
	}
}

// Mixed-Dep wiring exercises every constructor path in one Needs
// call. A regression where one of the types stops satisfying [Dep]
// surfaces as a compile error, not a runtime failure.
func TestDep_MixedTypesCompose(t *testing.T) {
	plan := sparkwing.NewPlan()
	a := sparkwing.Job(plan, "a", &buildJob{})
	gate := sparkwing.JobApproval(plan, "review", sparkwing.ApprovalConfig{
		OnExpiry: sparkwing.ApprovalDeny,
	})
	group := sparkwing.GroupJobs(plan, "shards", a)
	leaf := sparkwing.Job(plan, "leaf", &buildJob{}).
		Needs(a, gate, group, sparkwing.NodeIDOf("a"))

	deps := leaf.DepIDs()
	// "a" appears via *JobNode, via the group containing it, and
	// via NodeIDOf — addNeed dedups to a single entry.
	got := map[string]struct{}{}
	for _, d := range deps {
		got[d] = struct{}{}
	}
	for _, want := range []string{"a", "review"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing dep %q in %v", want, deps)
		}
	}
}
