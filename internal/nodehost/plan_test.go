package nodehost

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestSDKPlanningUnknownPipelineNeverPlans(t *testing.T) {
	planner, input, fixture, processes := sdkPlanningFixture(t)
	input.Run.Pipeline = "ghost"
	_, err := planner.Prepare(t.Context(), input, fixture.input)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.UnknownPipeline {
		t.Fatalf("error=%v", err)
	}
	if len(*processes) != 1 {
		t.Fatal("unknown pipeline started planning or populated the cache")
	}
	process := (*processes)[0]
	if process.closed != 1 || process.cleaned != 1 {
		t.Fatal("unknown pipeline leaked its describe process")
	}
	for _, request := range process.requests {
		if request.Op == "plan" {
			t.Fatal("unknown pipeline reached planning")
		}
	}
}
