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

// Defect 7 (plan-time guard): a cost above the group's capacity can
// never be admitted, so the SDK rejects it at Plan time rather than
// letting the node strand in the queue forever.
func TestConcurrency_PanicsWhenCostExceedsCapacity(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{Capacity: 4})
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on cost > capacity")
		}
	}()
	sparkwing.Job(plan, "x", &buildJob{}).Concurrency(g, 5)
}

// An unset capacity defaults to 1 at the backend, so any cost above 1
// against an unset-capacity group is also unadmittable.
func TestConcurrency_PanicsWhenCostExceedsDefaultCapacity(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on cost > default capacity 1")
		}
	}()
	sparkwing.Job(plan, "x", &buildJob{}).Concurrency(g, 2)
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
	if plan.ConcurrencyCost() != 1 {
		t.Fatalf("plan cost = %d, want default 1", plan.ConcurrencyCost())
	}
}

func TestPlanConcurrency_ComposesMultipleGroups(t *testing.T) {
	plan := sparkwing.NewPlan()
	land := sparkwing.NewConcurrencyGroup("land", sparkwing.ConcurrencyLimit{Capacity: 1})
	memory := sparkwing.NewConcurrencyGroup("memory-gb", sparkwing.ConcurrencyLimit{Capacity: 32})
	plan.Concurrency(land)
	plan.Concurrency(memory, 8)

	memberships := plan.PlanConcurrency()
	if len(memberships) != 2 {
		t.Fatalf("plan memberships = %d, want 2", len(memberships))
	}
	if memberships[0].Group != land || memberships[0].Cost != 1 {
		t.Fatalf("first membership = %+v, want land cost 1", memberships[0])
	}
	if memberships[1].Group != memory || memberships[1].Cost != 8 {
		t.Fatalf("second membership = %+v, want memory cost 8", memberships[1])
	}
}

func TestPlanConcurrency_ReplacesSameGroup(t *testing.T) {
	plan := sparkwing.NewPlan()
	group := sparkwing.NewConcurrencyGroup("memory-gb", sparkwing.ConcurrencyLimit{Capacity: 32})
	plan.Concurrency(group, 4)
	plan.Concurrency(group, 8)

	memberships := plan.PlanConcurrency()
	if len(memberships) != 1 {
		t.Fatalf("plan memberships = %d, want 1", len(memberships))
	}
	if memberships[0].Cost != 8 {
		t.Fatalf("cost = %d, want replacement cost 8", memberships[0].Cost)
	}
}

func TestConcurrencyGroup_HostAdmissionRequiresScopeBox(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic")
		}
	}()
	sparkwing.NewConcurrencyGroup("host", sparkwing.ConcurrencyLimit{HostAdmission: true})
}

func TestNodeConcurrency_RejectsHostAdmissionGroup(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("host", sparkwing.ConcurrencyLimit{
		Capacity:      1,
		Scope:         sparkwing.ScopeBox,
		HostAdmission: true,
	})
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic")
		}
	}()
	sparkwing.Job(plan, "x", &buildJob{}).Concurrency(g)
}

func TestPlanConcurrency_HostAdmissionRecorded(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("host", sparkwing.ConcurrencyLimit{
		Capacity:      1,
		Scope:         sparkwing.ScopeBox,
		HostAdmission: true,
	})
	plan.Concurrency(g)
	if !plan.HostAdmission() {
		t.Fatalf("plan HostAdmission = false, want true")
	}
}

func TestPlanConcurrency_RejectsMultipleHostAdmissionGroups(t *testing.T) {
	plan := sparkwing.NewPlan()
	first := sparkwing.NewConcurrencyGroup("host-a", sparkwing.ConcurrencyLimit{
		Capacity:      1,
		Scope:         sparkwing.ScopeBox,
		HostAdmission: true,
	})
	second := sparkwing.NewConcurrencyGroup("host-b", sparkwing.ConcurrencyLimit{
		Capacity:      1,
		Scope:         sparkwing.ScopeBox,
		HostAdmission: true,
	})
	plan.Concurrency(first)
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic")
		}
	}()
	plan.Concurrency(second)
}

func TestPlanConcurrency_ExplicitCost(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("box-budget", sparkwing.ConcurrencyLimit{Capacity: 8})
	plan.Concurrency(g, 4)
	if plan.ConcurrencyGroupRef() != g {
		t.Fatalf("plan group not recorded")
	}
	if plan.ConcurrencyCost() != 4 {
		t.Fatalf("plan cost = %d, want 4", plan.ConcurrencyCost())
	}
}

func TestPlanConcurrency_NonPositiveCostClampsToOne(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("box-budget", sparkwing.ConcurrencyLimit{Capacity: 2})
	plan.Concurrency(g, 0)
	if plan.ConcurrencyCost() != 1 {
		t.Fatalf("plan cost = %d, want clamped 1", plan.ConcurrencyCost())
	}
}

func TestPlanConcurrency_NilGroupClears(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("box-budget", sparkwing.ConcurrencyLimit{Capacity: 2})
	plan.Concurrency(g)
	plan.Concurrency(nil)
	if plan.ConcurrencyGroupRef() != nil {
		t.Fatalf("Concurrency(nil) should clear plan membership")
	}
	if plan.ConcurrencyCost() != 0 {
		t.Fatalf("plan cost = %d, want 0 after clear", plan.ConcurrencyCost())
	}
}

func TestPlanConcurrency_PanicsWhenCostExceedsCapacity(t *testing.T) {
	plan := sparkwing.NewPlan()
	g := sparkwing.NewConcurrencyGroup("box-budget", sparkwing.ConcurrencyLimit{Capacity: 4})
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on plan cost > capacity")
		}
	}()
	plan.Concurrency(g, 5)
}
