package sparkwingruntime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type guardingPipe struct{ sparkwing.Base }

func (guardingPipe) Plan(ctx context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwingruntime.GuardPlanTime(ctx, "test.helper")
	return nil
}

func TestPlanTime_GuardPanicsUnderPlanContext(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("plantime-guard-test", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return guardingPipe{}
	})
	reg, _ := sparkwing.Lookup("plantime-guard-test")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("GuardPlanTime should panic when called under a plan-time ctx")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Plan() must be pure-declarative") {
			t.Fatalf("panic message missing canonical text, got: %s", msg)
		}
		if !strings.Contains(msg, "test.helper") {
			t.Fatalf("panic message should name the helper, got: %s", msg)
		}
	}()
	_, _ = reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "plantime-guard-test"})
}

func TestPlanTime_GuardSilentOutsidePlanContext(t *testing.T) {
	sparkwingruntime.GuardPlanTime(context.Background(), "test.helper")
}

func TestPlanTime_IsPlanTimeFalseByDefault(t *testing.T) {
	if sparkwingruntime.IsPlanTime(context.Background()) {
		t.Fatal("bare ctx should not be plan-time")
	}
}
