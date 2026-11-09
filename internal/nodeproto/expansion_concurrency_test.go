package nodeproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestExpansionPreservesConcurrencyDeclarations(t *testing.T) {
	group := nodewire.ConcurrencySpec{Group: "pool", Capacity: new(3), Cost: new(1), Scope: new("run"), OnLimit: new("queue")}
	ctx := planContext()
	ctx.Expansion, ctx.SourceJob = true, "source"
	ctx.ExistingJobs = []nodewire.JobSpec{{
		ID: "source", Steps: []nodewire.StepSpec{{ID: "run"}},
		Closures: nodewire.ClosureSpec{Generate: []string{"generator"}}, Concurrency: []nodewire.ConcurrencySpec{group},
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
			raw, err := json.Marshal(nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{
				ID: "child", Groups: []string{"generator"},
				Steps: []nodewire.StepSpec{{ID: "run"}}, Concurrency: []nodewire.ConcurrencySpec{changed},
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
			planContext.ExistingJobs = append([]nodewire.JobSpec(nil), ctx.ExistingJobs...)
			planContext.ExistingJobs[0].Concurrency = nil
			planContext.ExistingConcurrency = []nodewire.ConcurrencySpec{group}
			t.Run("plan-membership", func(t *testing.T) { checkPlan(t, string(raw), planContext, "") })
		})
	}
}
