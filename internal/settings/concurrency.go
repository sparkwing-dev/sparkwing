package settings

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ResolveConcurrency preserves declarations and fills omitted policy fields.
func ResolveConcurrency(declarations []nodeproto.ConcurrencySpec) ([]runspec.Concurrency, error) {
	result := make([]runspec.Concurrency, 0, len(declarations))
	seen := map[string]bool{}
	for i, declaration := range declarations {
		group := runspec.Concurrency{Group: declaration.Group, Scope: "global", Capacity: 1, Cost: 1, OnLimit: "queue", QueueTimeoutMS: declaration.QueueTimeoutMS}
		if declaration.Scope != nil {
			group.Scope = *declaration.Scope
		}
		if declaration.Capacity != nil {
			group.Capacity = *declaration.Capacity
		}
		if declaration.Cost != nil {
			group.Cost = *declaration.Cost
		}
		if declaration.OnLimit != nil {
			group.OnLimit = *declaration.OnLimit
		}
		if group.Group == "" || seen[group.Group] || group.Capacity < 1 || group.Cost < 1 || group.Cost > group.Capacity || group.Scope != "global" && group.Scope != "run" && group.Scope != "box" || group.OnLimit != "queue" && group.OnLimit != "skip" && group.OnLimit != "fail" && group.OnLimit != "cancel_others" {
			return nil, fmt.Errorf("concurrency[%d]: invalid group policy", i)
		}
		if _, err := milliseconds(group.QueueTimeoutMS, "concurrency"); err != nil {
			return nil, err
		}
		seen[group.Group] = true
		result = append(result, group)
	}
	return result, nil
}

func checkConcurrencyGroups(settings runspec.ExecutionSettings) error {
	groups := map[string]runspec.Concurrency{}
	check := func(memberships []runspec.Concurrency) error {
		for _, group := range memberships {
			if previous, ok := groups[group.Group]; ok && (previous.Capacity != group.Capacity || previous.Scope != group.Scope || previous.OnLimit != group.OnLimit) {
				return fmt.Errorf("concurrency: group policy mismatch")
			}
			groups[group.Group] = group
		}
		return nil
	}
	for _, job := range settings.Jobs {
		if err := check(job.Concurrency); err != nil {
			return err
		}
	}
	return nil
}
