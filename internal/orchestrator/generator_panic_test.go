package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestGeneratorLookupPreservesPanicDiagnostic(t *testing.T) {
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "discover", func(context.Context) error { return nil })
	exp := sparkwing.Expansion{Source: source, Gen: func(context.Context) []*sparkwing.JobNode { panic("invalid concurrency cost") }}
	nodes, err := invokeGeneratorForPod(t.Context(), exp)
	if err == nil || !strings.Contains(err.Error(), "invalid concurrency cost") || !strings.Contains(err.Error(), "discover") {
		t.Fatalf("generator diagnostic lost: nodes=%v err=%v", nodes, err)
	}
}

func TestGeneratorLookupKeepsSuccessfulChildren(t *testing.T) {
	plan := sparkwing.NewPlan()
	child := sparkwing.Job(plan, "child", func(context.Context) error { return nil })
	exp := sparkwing.Expansion{Gen: func(context.Context) []*sparkwing.JobNode { return []*sparkwing.JobNode{child} }}
	nodes, err := invokeGeneratorForPod(t.Context(), exp)
	if err != nil || len(nodes) != 1 || nodes[0] != child {
		t.Fatalf("generator result=%v err=%v", nodes, err)
	}
}
