package sparkwing_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestJob_RejectsPathUnsafeIDs(t *testing.T) {
	for _, bad := range []string{"a/b", `a\b`, "..", ".", "x\nb", "x\x00y"} {
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
