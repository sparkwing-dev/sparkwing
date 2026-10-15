package nodeproto

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestExpansionPreservesExistingConcurrency(t *testing.T) {
	group := ConcurrencySpec{Group: "pool", Capacity: 3, Cost: 1, Scope: "run", OnLimit: "queue"}
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
				changed.Cost = 2
			case "capacity":
				changed.Capacity = 4
			case "scope":
				changed.Scope = "box"
			case "on_limit":
				changed.OnLimit = "fail"
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
			t.Run("job-membership", func(t *testing.T) { checkPlan(t, string(raw), ctx, code) })
			planContext := ctx
			planContext.ExistingJobs = append([]JobSpec(nil), ctx.ExistingJobs...)
			planContext.ExistingJobs[0].Concurrency = nil
			planContext.ExistingConcurrency = []ConcurrencySpec{group}
			t.Run("plan-membership", func(t *testing.T) { checkPlan(t, string(raw), planContext, code) })
		})
	}
}
