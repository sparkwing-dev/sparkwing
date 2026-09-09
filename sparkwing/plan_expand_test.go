package sparkwing

import (
	"context"
	"reflect"
	"testing"
)

func TestInsertExpandedRejectsWholeBatch(t *testing.T) {
	for _, failure := range []string{"existing id", "batch duplicate", "nil child"} {
		t.Run(failure, func(t *testing.T) {
			plan := NewPlan()
			noop := func(context.Context) error { return nil }
			source := Job(plan, "source", noop)
			existing := Job(plan, "existing", noop)
			fresh := NewDetachedNode("fresh", &jobFn{fn: noop})
			var invalid *JobNode
			switch failure {
			case "existing id":
				invalid = NewDetachedNode("existing", &jobFn{fn: noop})
			case "batch duplicate":
				invalid = NewDetachedNode("fresh", &jobFn{fn: noop})
			}
			before := plan.Nodes()
			if err := plan.insertExpanded(source, []*JobNode{fresh, invalid}); err == nil {
				t.Fatal("accepted invalid batch")
			}
			if !reflect.DeepEqual(plan.Nodes(), before) || plan.Job("fresh") != nil {
				t.Error("rejected batch left phantom node in plan")
			}
			if len(fresh.DepIDs()) != 0 {
				t.Errorf("rejected batch mutated child dependencies: %v", fresh.DepIDs())
			}
			if plan.Job("existing") != existing {
				t.Error("replaced existing node")
			}
			if err := plan.insertExpanded(source, []*JobNode{fresh}); err != nil {
				t.Fatalf("failed batch poisoned retry: %v", err)
			}
			if plan.Job("fresh") != fresh || !reflect.DeepEqual(fresh.DepIDs(), []string{"source"}) {
				t.Fatal("successful retry did not insert dependent child")
			}
		})
	}
}
