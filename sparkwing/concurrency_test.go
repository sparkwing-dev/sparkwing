package sparkwing

import (
	"context"
	"testing"
)

func TestJobConcurrencyReachesPlanDocument(t *testing.T) {
	plan := NewPlan()
	group := NewConcurrencyGroup("db", ConcurrencyLimit{Capacity: 8, OnLimit: Queue})
	Job(plan, "build", func(context.Context) error { return nil }).Concurrency(group, 4)
	memberships := planDocument(plan).Jobs[0].Concurrency
	if len(memberships) != 1 || memberships[0].Group != "db" || memberships[0].Cost != 4 || memberships[0].Capacity != 8 {
		t.Fatalf("job concurrency = %+v", memberships)
	}
}

func TestPlanConcurrencyComposesAndReplacesGroups(t *testing.T) {
	plan := NewPlan()
	land := NewConcurrencyGroup("land", ConcurrencyLimit{Capacity: 1})
	memory := NewConcurrencyGroup("memory", ConcurrencyLimit{Capacity: 32})
	plan.Concurrency(land).Concurrency(memory, 4).Concurrency(memory, 8)
	memberships := planDocument(plan).Concurrency
	if len(memberships) != 2 || memberships[0].Group != "land" || memberships[0].Cost != 1 || memberships[1].Group != "memory" || memberships[1].Cost != 8 {
		t.Fatalf("plan concurrency = %+v", memberships)
	}
}
