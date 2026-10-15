package sparkwing

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestJobFanOutStaticMembers(t *testing.T) {
	plan := NewPlan()
	group := JobFanOut(plan, "builds", []string{"a", "b"}, func(item string) (string, any) {
		return "build-" + item, func(context.Context) error { return nil }
	})
	if group.Name() != "builds" || len(group.Members()) != 2 || plan.byID["build-a"] == nil || plan.byID["build-b"] == nil {
		t.Fatalf("fan-out members = %+v", group.Members())
	}
}

func TestEmptyJobFanOutSatisfiesDependency(t *testing.T) {
	plan := NewPlan()
	group := JobFanOut(plan, "empty", []int{}, func(int) (string, any) { t.Fatal("callback ran"); return "", nil })
	consumer := Job(plan, "after", func(context.Context) error { return nil }).Needs(group)
	if len(group.Members()) != 0 || len(consumer.needs) != 0 {
		t.Fatalf("empty fan-out left dependencies: %v", consumer.needs)
	}
}

func TestGroupModifiersApplyToEveryMember(t *testing.T) {
	plan := NewPlan()
	upstream := Job(plan, "upstream", func(context.Context) error { return nil })
	group := JobFanOut(plan, "builds", []string{"a", "b"}, func(item string) (string, any) {
		return "build-" + item, func(context.Context) error { return nil }
	}).Needs(upstream).Retry(3).Timeout(10 * time.Second).NoProgressTimeout(2 * time.Second)
	for _, member := range group.Members() {
		if !slices.Equal(member.needs, []string{"upstream"}) || member.retryAttempts != 3 || member.timeout != 10*time.Second || member.noProgressTimeout != 2*time.Second {
			t.Fatalf("group member %s settings = %+v", member.id, member)
		}
	}
}
