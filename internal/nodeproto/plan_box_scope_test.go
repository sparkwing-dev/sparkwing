package nodeproto

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestPlanBoxScopeRefusedAtEveryPlacement(t *testing.T) {
	for _, placement := range []Placement{PlacementLocal, PlacementLocalTriggered, PlacementRunnerHosted, PlacementLauncher, PlacementHere, PlacementGitHubActions} {
		t.Run(string(placement), func(t *testing.T) {
			ctx := planContext()
			ctx.Placement = placement
			_, err := ValidatePlan([]byte(`{"jobs":[],"concurrency":[{"group":"deploys","scope":"box"}]}`), ctx)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid || !strings.Contains(err.Error(), "deploys") || !strings.Contains(err.Error(), "job-level box scope") {
				t.Fatalf("plan box refusal must name the group and alternative: %v", err)
			}
			checkPlan(t, `{"jobs":[{"id":"job","steps":[{"id":"body"}],"concurrency":[{"group":"deploys","scope":"box"}]}]}`, ctx, "")
		})
	}
}
