package sparkwing_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestConcurrency_NotCalledLeavesNoMembership(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{})
	if n.ConcurrencyGroupRef() != nil {
		t.Fatalf("a node without Concurrency() should have no group")
	}
	if n.ConcurrencyCost() != 0 {
		t.Fatalf("cost = %d, want 0 with no membership", n.ConcurrencyCost())
	}
}

func TestConcurrency_DefaultCostIsOne(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{
		Capacity: 2,
		OnLimit:  sparkwing.Queue,
	})
	n := sparkwing.Job(plan, "x", &buildJob{}).Concurrency(g)
	if n.ConcurrencyGroupRef() != g {
		t.Fatalf("group not recorded on node")
	}
	if n.ConcurrencyCost() != 1 {
		t.Fatalf("cost = %d, want default 1", n.ConcurrencyCost())
	}
}

func TestConcurrency_ExplicitCost(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{Capacity: 8})
	n := sparkwing.Job(plan, "x", &buildJob{}).Concurrency(g, 4)
	if n.ConcurrencyCost() != 4 {
		t.Fatalf("cost = %d, want 4", n.ConcurrencyCost())
	}
}

func TestConcurrency_NonPositiveCostClampsToOne(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{Capacity: 2})
	n := sparkwing.Job(plan, "x", &buildJob{}).Concurrency(g, 0)
	if n.ConcurrencyCost() != 1 {
		t.Fatalf("cost = %d, want clamped 1", n.ConcurrencyCost())
	}
}

func TestConcurrency_PanicsOnMultipleCosts(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{Capacity: 2})
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on more than one cost argument")
		}
	}()
	sparkwing.Job(plan, "x", &buildJob{}).Concurrency(g, 1, 2)
}

func TestConcurrency_NilGroupClears(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{Capacity: 2})
	n := sparkwing.Job(plan, "x", &buildJob{}).Concurrency(g)
	n.Concurrency(nil)
	if n.ConcurrencyGroupRef() != nil {
		t.Fatalf("Concurrency(nil) should clear membership")
	}
}

func TestConcurrencyGroup_LimitDefaults(t *testing.T) {
	g := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{Capacity: 3})
	if g.Name() != "db" {
		t.Fatalf("Name = %q, want db", g.Name())
	}
	limit := g.Limit()
	if limit.Capacity != 3 {
		t.Fatalf("Capacity = %d, want 3", limit.Capacity)
	}
	// The zero values read as Global scope and Queue policy at the
	// coordination layer.
	if limit.Scope != "" && limit.Scope != sparkwing.ScopeGlobal {
		t.Fatalf("unexpected scope %q", limit.Scope)
	}
}

func TestPlanConcurrency_RecordsGroup(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("prod-deploys", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Fail,
	})
	plan.Concurrency(g)
	if plan.ConcurrencyGroupRef() != g {
		t.Fatalf("plan group not recorded")
	}
}
