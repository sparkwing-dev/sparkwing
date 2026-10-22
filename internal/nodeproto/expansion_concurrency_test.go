package nodeproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestExpansionPreservesConcurrencyDeclarations(t *testing.T) {
	group := ConcurrencySpec{Group: "pool", Capacity: new(3), Cost: new(1), Scope: new("run"), OnLimit: new("queue")}
	ctx := planContext()
	ctx.Expansion, ctx.SourceJob = true, "source"
	ctx.ExistingJobs = []JobSpec{{
		ID: "source", Steps: []StepSpec{{ID: "run"}},
		Closures: ClosureSpec{Generate: []string{"generator"}}, Concurrency: []ConcurrencySpec{group},
	}}
	for _, field := range []string{"cost", "capacity", "scope", "on_limit"} {
		t.Run(field, func(t *testing.T) {
			changed := group
			switch field {
			case "cost":
				changed.Cost = new(2)
			case "capacity":
				changed.Capacity = new(4)
			case "scope":
				changed.Scope = new("box")
			case "on_limit":
				changed.OnLimit = new("fail")
			}
			raw, err := json.Marshal(PlanDoc{Jobs: []JobSpec{{
				ID: "child", Groups: []string{"generator"},
				Steps: []StepSpec{{ID: "run"}}, Concurrency: []ConcurrencySpec{changed},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			code := apierr.PlanInvalid
			if field == "cost" {
				code = ""
			}
			check := func(t *testing.T, context ValidationContext) {
				t.Helper()
				checkPlan(t, string(raw), context, code)
				if code != "" {
					_, err := ValidatePlan(raw, context)
					var failure *apierr.Error
					if !errors.As(err, &failure) || !strings.Contains(failure.Message, "jobs[0].concurrency[0]."+field) {
						t.Fatalf("mismatch lost its field path: %v", err)
					}
				}
			}
			t.Run("job-membership", func(t *testing.T) { check(t, ctx) })
			planContext := ctx
			planContext.ExistingJobs = append([]JobSpec(nil), ctx.ExistingJobs...)
			planContext.ExistingJobs[0].Concurrency = nil
			planContext.ExistingConcurrency = []ConcurrencySpec{group}
			t.Run("plan-membership", func(t *testing.T) { checkPlan(t, string(raw), planContext, "") })
		})
	}
}
