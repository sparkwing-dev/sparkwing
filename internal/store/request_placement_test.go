package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestAcceptPlanUsesEveryRequestPlacement(t *testing.T) {
	for _, test := range []struct {
		placement nodeproto.Placement
		origin    runspec.Origin
		dispatch  runspec.Dispatch
		location  string
		here      bool
	}{
		{nodeproto.PlacementLocal, runspec.OriginLocal, runspec.DispatchHost, "local", false},
		{nodeproto.PlacementLocalTriggered, runspec.OriginTrigger, runspec.DispatchHost, "local", false},
		{nodeproto.PlacementRunnerHosted, runspec.OriginTrigger, runspec.DispatchHost, "controller", false},
		{nodeproto.PlacementLauncher, runspec.OriginTrigger, runspec.DispatchController, "controller", false},
		{nodeproto.PlacementHere, runspec.OriginTrigger, runspec.DispatchHost, "controller", true},
		{nodeproto.PlacementGitHubActions, runspec.OriginActions, runspec.DispatchHost, "controller", false},
	} {
		t.Run(string(test.placement), func(t *testing.T) {
			tenant, acceptance, _ := planFixtureWithInput(t, test.dispatch, func(in *IntakeRequest) {
				in.Request.Origin, in.Request.ExecutionLocation = test.origin, test.location
				if test.here {
					in.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
				}
			})
			acceptance.Seal.Plan = []byte(`{"jobs":[{"id":"job","steps":[{"id":"run"}]}]}`)
			accepted, err := tenant.AcceptPlan(t.Context(), acceptance)
			if err != nil {
				t.Fatal(err)
			}
			if got := RequestPlacement(accepted.Spec.Request.Request); got != test.placement {
				t.Fatalf("accepted placement = %q, want %q", got, test.placement)
			}
		})
	}
}
