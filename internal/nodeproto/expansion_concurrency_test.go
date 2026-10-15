package nodeproto

import (
	"encoding/json"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"testing"
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
			code := apierr.Code("")
			t.Run("job-membership", func(t *testing.T) { checkPlan(t, string(raw), ctx, code) })
			planContext := ctx
			planContext.ExistingJobs = append([]JobSpec(nil), ctx.ExistingJobs...)
			planContext.ExistingJobs[0].Concurrency = nil
			planContext.ExistingConcurrency = []ConcurrencySpec{group}
			t.Run("plan-membership", func(t *testing.T) { checkPlan(t, string(raw), planContext, code) })
		})
	}
}
