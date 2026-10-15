package orchestrator

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// The plan document carries the checkout the plan asked for, which the host
// reads to prepare the node's source.
func TestPlanDocument_CarriesTheCheckout(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "a", func(context.Context) error { return nil })
	plan.Checkout(sparkwing.Checkout{Depth: testOrchestratorPtr(3), LFS: testOrchestratorPtr(true)})
	doc := sdkserve.DefaultOperations.Document(plan)
	if doc.Source == nil || doc.Source.Depth == nil || *doc.Source.Depth != 3 || doc.Source.LFS == nil || !*doc.Source.LFS {
		t.Fatalf("source = %+v", doc.Source)
	}
}

func testOrchestratorPtr[T any](value T) *T { return &value }
