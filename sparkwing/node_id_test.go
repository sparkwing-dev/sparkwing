package sparkwing_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestJob_RejectsPathUnsafeIDs(t *testing.T) {
	for _, bad := range []string{`a\b`, "..", ".", "x\nb", "x\x00y", "a//b", "../x", "a/.."} {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Errorf("Job(%q) did not panic", bad)
					return
				}
				if !strings.HasPrefix(r.(string), "sparkwing: Job(") {
					t.Errorf("Job(%q) panic %q does not point at the call site", bad, r)
				}
			}()
			plan := sparkwing.NewPlan()
			sparkwing.Job(plan, bad, &buildJob{})
		}()
	}
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "build.amd64-v2", &buildJob{})
}

func TestNewConcurrencyGroup_RejectsAnEmptyName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewConcurrencyGroup with an empty name did not panic")
		}
	}()
	sparkwing.NewConcurrencyGroup("", sparkwing.ConcurrencyLimit{Capacity: 1})
}
