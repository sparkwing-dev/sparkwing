package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type planningFixture struct {
	input PlanningInput
	raw   json.RawMessage
	plans int
}

func newPlanningFixture(t *testing.T) *planningFixture {
	t.Helper()
	return &planningFixture{raw: json.RawMessage(`{ "jobs": [{"id": "test", "steps":[{"id":"body"}]}] }`), input: PlanningInput{Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: planningSecretMatcher{}}}}
}

func (f *planningFixture) functions() func(context.Context) (json.RawMessage, error) {
	return func(context.Context) (json.RawMessage, error) { f.plans++; return f.raw, nil }
}

type planningSecretMatcher struct{}

func (planningSecretMatcher) ContainsSecret(value string) bool { return false }

func TestPlanInitialAlwaysRunsCurrentSDKPlanning(t *testing.T) {
	calls := 0
	for range 2 {
		result, err := planInitial(t.Context(), PlanningInput{Validation: nodeproto.ValidationContext{Secrets: planningSecretMatcher{}, Placement: nodeproto.PlacementLocal}}, func(context.Context) (json.RawMessage, error) { calls++; return json.RawMessage(`{"jobs":[]}`), nil })
		if err != nil || len(result.Plan.Jobs) != 0 || result.PlanHash == "" {
			t.Fatalf("planning = %+v, %v", result, err)
		}
	}
	if calls != 2 {
		t.Fatalf("SDK calls = %d, want 2", calls)
	}
}

func TestPlanInitialRetainsAuthorityLossAroundSDKPlanning(t *testing.T) {
	for _, stage := range []string{"before", "plan"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newPlanningFixture(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			functions := fixture.functions()
			switch stage {
			case "before":
				cancel(lease.ErrLeaseLost)
			case "plan":
				plan := functions
				functions = func(ctx context.Context) (json.RawMessage, error) {
					cancel(lease.ErrLeaseLost)
					return plan(ctx)
				}
			}
			result, err := planInitial(ctx, fixture.input, functions)
			if !errors.Is(err, lease.ErrLeaseLost) || len(result.CanonicalPlan) != 0 {
				t.Fatalf("%s lost authority: result=%+v error=%v", stage, result, err)
			}
		})
	}
}
