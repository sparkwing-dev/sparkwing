package orchestrator

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// The plan document carries the checkout the plan asked for, which the host
// reads to prepare the node's source.
func TestPlanDocument_CarriesTheCheckout(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "a", func(context.Context) error { return nil })
	plan.CheckoutOld(sparkwing.CheckoutOld{Depth: 3, LFS: true})
	doc, err := planDocument(plan, sparkwing.RunContext{RunID: "run-1", Pipeline: "demo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source == nil || *doc.Source != (nodeproto.PlanSource{Depth: 3, LFS: true}) {
		t.Fatalf("source = %+v", doc.Source)
	}
}
