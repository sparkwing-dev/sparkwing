package jobs

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestTemplateVerifyPlanUsesMeasuredResourcesAndBoundsFanout(t *testing.T) {
	plan := sparkwing.NewPlan()
	if err := (TemplateVerify{}).Plan(context.Background(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	if hints := plan.ResourceHints(); hints != nil {
		t.Fatalf("resource pin = %#v, want measured admission", hints)
	}

	count := 0
	for _, node := range plan.Nodes() {
		if hints := node.ResourceHints(); hints != nil {
			t.Fatalf("%s resource pin = %#v, want measured admission", node.ID(), hints)
		}
		if !strings.HasPrefix(node.ID(), "verify-") {
			continue
		}
		count++
		group := node.ConcurrencyGroupRef()
		if group == nil {
			t.Fatalf("%s has no fanout bound", node.ID())
		}
		limit := group.Limit()
		if group.Name() != "template-verify" || limit.Scope != sparkwing.ScopeRun || limit.OnLimit != sparkwing.Queue || limit.Capacity != 4 {
			t.Fatalf("%s concurrency = %q %+v", node.ID(), group.Name(), limit)
		}
	}
	if count != len(verifyTemplates) {
		t.Fatalf("bounded templates = %d, want %d", count, len(verifyTemplates))
	}
}

func TestRequireTemplateVerifyDisk_FailsBeforeWorkBelowFloor(t *testing.T) {
	err := requireTemplateVerifyDisk(9 << 30)
	if err == nil || !strings.Contains(err.Error(), "10 GiB") {
		t.Fatalf("low-disk preflight = %v, want actionable 10 GiB floor", err)
	}
	if err := requireTemplateVerifyDisk(10 << 30); err != nil {
		t.Fatalf("disk at floor rejected: %v", err)
	}
}
