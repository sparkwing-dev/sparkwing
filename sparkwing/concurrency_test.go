package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestJobConcurrencyReachesPlanDocument(t *testing.T) {
	plan := NewPlan()
	group := NewConcurrencyGroup("db", ConcurrencyLimit{Capacity: 8, OnLimit: Queue})
	Job(plan, "build", func(context.Context) error { return nil }).Concurrency(group, 4)
	memberships := planDocument(plan).Jobs[0].Concurrency
	if len(memberships) != 1 || memberships[0].Group != "db" || *memberships[0].Cost != 4 || *memberships[0].Capacity != 8 {
		t.Fatalf("job concurrency = %+v", memberships)
	}
}

func TestGoConcurrencyCapacityMustBeExplicit(t *testing.T) {
	for _, capacity := range []int{0, 1} {
		plan := NewPlan()
		Job(plan, "deploy", func(context.Context) error { return nil }).Concurrency(NewConcurrencyGroup("deploys", ConcurrencyLimit{Capacity: capacity}))
		raw, err := json.Marshal(planDocument(plan))
		if err != nil {
			t.Fatal(err)
		}
		_, err = nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker()})
		if capacity == 0 {
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid {
				t.Fatalf("zero capacity = %v, want plan_invalid", err)
			}
		} else if err != nil {
			t.Fatalf("capacity %d: %v", capacity, err)
		}
	}
}

func TestPlanConcurrencyComposesAndReplacesGroups(t *testing.T) {
	plan := NewPlan()
	land := NewConcurrencyGroup("land", ConcurrencyLimit{Capacity: 1})
	memory := NewConcurrencyGroup("memory", ConcurrencyLimit{Capacity: 32})
	plan.Concurrency(land).Concurrency(memory, 4).Concurrency(memory, 8)
	memberships := planDocument(plan).Concurrency
	if len(memberships) != 2 || memberships[0].Group != "land" || memberships[0].Cost != nil || memberships[1].Group != "memory" || *memberships[1].Cost != 8 {
		t.Fatalf("plan concurrency = %+v", memberships)
	}
}

func TestGoConcurrencyKeepsOptionalPoliciesUnset(t *testing.T) {
	plan := NewPlan()
	group := NewConcurrencyGroup("gate", ConcurrencyLimit{Capacity: 2})
	Job(plan, "job", func(context.Context) error { return nil }).Concurrency(group)
	spec := planDocument(plan).Jobs[0].Concurrency[0]
	if spec.Scope != nil || spec.OnLimit != nil || spec.Cost != nil || spec.Capacity == nil || *spec.Capacity != 2 {
		t.Fatalf("Go declaration presence: %+v", spec)
	}
}
