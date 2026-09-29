package orchestrator

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func claimTestPlan(t *testing.T, message string) *sparkwing.Plan {
	t.Helper()
	plan := sparkwing.NewPlan()
	a := sparkwing.Job(plan, "a", func(context.Context) error { return nil })
	sparkwing.Job(plan, "b", func(context.Context) error { return nil }).Needs(a).Retry(len(message))
	plan.Checkout(sparkwing.Checkout{Depth: 3, LFS: true})
	return plan
}

// A node's hash is the same however often its pipeline plans it and changes
// with the node's spec, which is what lets a pod refuse a node the accepted
// plan does not hold.
func TestClaimPlanSnapshot_HashesEachNodeStably(t *testing.T) {
	rc := sparkwing.RunContext{RunID: "run-1", Pipeline: "demo"}
	first, err := claimPlanSnapshot(claimTestPlan(t, "x"), rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := claimNodeSpecHash(claimTestPlan(t, "x"), rc, nil, "b")
	if err != nil || again != first.Nodes[1].SpecHash || first.Nodes[0].SpecHash == first.Nodes[1].SpecHash {
		t.Fatalf("hashes %q, %q; replanned b = %q, %v", first.Nodes[0].SpecHash, first.Nodes[1].SpecHash, again, err)
	}
	changed, err := claimNodeSpecHash(claimTestPlan(t, "xy"), rc, nil, "b")
	if err != nil || changed == again {
		t.Fatalf("a changed spec kept its hash: %q %v", changed, err)
	}
	if unchanged, _ := claimNodeSpecHash(claimTestPlan(t, "xy"), rc, nil, "a"); unchanged != first.Nodes[0].SpecHash {
		t.Fatalf("an unchanged node's hash moved: %q", unchanged)
	}
	if first.Source == nil || *first.Source != (snapshotSource{Depth: 3, LFS: true}) {
		t.Fatalf("source = %+v", first.Source)
	}
	if _, err := claimNodeSpecHash(claimTestPlan(t, "x"), rc, nil, "missing"); err == nil {
		t.Fatal("a node the plan lacks has a hash")
	}
}
