package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestSchedulingGuardRefusalNeverReleasesWork(t *testing.T) {
	graph := ScheduleSnapshot{GuardRefusal: "branch guard refused", Nodes: []ScheduleNode{
		{ID: "source", Status: "pending", Expansion: "expected"},
		{ID: "dependent", Status: "pending", Deps: []string{"source"}},
	}}
	decision := Decide(graph)
	if decision.Outcome != "failed" || decision.Reason != graph.GuardRefusal || len(decision.Nodes) != 2 {
		t.Fatalf("guard decision: %+v", decision)
	}
	for _, node := range decision.Nodes {
		if node.Kind != DecisionTerminal || node.Outcome != "skipped" {
			t.Fatalf("guard released work: %+v", node)
		}
	}
	graph.CancelRequested = true
	if got := Decide(graph); got.Outcome != "cancelled" {
		t.Fatalf("cancellation lost to guard: %+v", got)
	}
}

func TestSchedulingProjectsSealedGuardRefusal(t *testing.T) {
	snapshot := RunSnapshot{Run: RunRow{ID: "run", Plan: []byte(`{"jobs":[]}`), Spec: &runspec.Spec{Resolved: runspec.Resolved{Guards: runspec.Guards{When: []string{"git:branch=default"}, Passed: false}}}}}
	graph, err := snapshot.Scheduling()
	if err != nil || graph.GuardRefusal == "" || Decide(graph).Outcome != "failed" {
		t.Fatalf("sealed refusal: %+v, %v", graph, err)
	}
	snapshot.Run.Spec.Resolved.Guards.Passed = true
	graph, err = snapshot.Scheduling()
	if err != nil || graph.GuardRefusal != "" || Decide(graph).Outcome != "success" {
		t.Fatalf("passing guard: %+v, %v", graph, err)
	}
}
