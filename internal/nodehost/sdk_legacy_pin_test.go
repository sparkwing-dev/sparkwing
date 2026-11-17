package nodehost

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestOldSDKPlanFailureNamesUpgradeWithoutExposingPanic(t *testing.T) {
	for _, version := range []string{"v0.1.0", "v0.2.0", "v0.3.0"} {
		t.Run(version, func(t *testing.T) {
			planner, input, _, _ := sdkPlanningFixture(t)
			input.Entry.Engine.Identity.Pin = engineid.Pin{SDK: engineid.GoSDK, Version: version}
			failure := planner.planningError(context.Background(), nil, &apierr.Error{Code: apierr.PlanFailed, Message: "panic: private-nil-pointer"}, false, input)
			if strings.Contains(failure.Error(), "private-nil-pointer") {
				t.Fatal(failure)
			}
			upgrade := strings.Contains(failure.Error(), "sparkwing pipeline upgrade")
			if upgrade != (version != "v0.3.0") {
				t.Fatalf("SDK %s diagnostic: %v", version, failure)
			}
		})
	}
}
