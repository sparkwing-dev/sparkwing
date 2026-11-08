package jobs

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestTestPipelineMeasuresAndBoundsItsCPU(t *testing.T) {
	plan := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (&Test{}).Plan(context.Background(), plan, TestArgs{}, sparkwing.RunContext{Pipeline: "test"})
	})

	nodes := plan.Jobs
	if len(nodes) != 1 {
		t.Fatalf("planned nodes = %d, want 1", len(nodes))
	}
	if got := testGoCommand(hostShape{cpus: 14}); got != "GOMAXPROCS=6 go test -p 6 -timeout 25m ./..." {
		t.Fatalf("bounded command = %q", got)
	}
	if got := testGoCommand(hostShape{cpus: 4, singleTenant: true}); got != "GOMAXPROCS=1 go test -p 2 -timeout 25m ./..." {
		t.Fatalf("hosted four-core command = %q", got)
	}
}
