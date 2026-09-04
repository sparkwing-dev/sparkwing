package orchestrator

import (
	"context"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestPendingStoreNodePreservesPipelineLocalLockdown(t *testing.T) {
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "build", func(context.Context) error { return nil })
	record := pendingStoreNode("run", node, []string{"local"})
	if !reflect.DeepEqual(record.NeedsLabels, []string{"local"}) {
		t.Fatalf("pipeline local requirement became %v", record.NeedsLabels)
	}
}

func TestPendingStoreNodeComposesConflictingPipelineAndNodePlacement(t *testing.T) {
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "build", func(context.Context) error { return nil }).Requires("location=cloud")
	record := pendingStoreNode("run", node, []string{"local"})
	if !reflect.DeepEqual(record.NeedsLabels, []string{"location=cloud", "local"}) {
		t.Fatalf("composed requirements = %v", record.NeedsLabels)
	}
}
