package sparkwing_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Plan-time guard sentinel basics. Helper-specific guard coverage lives
// next to each helper (exec_test.go, docker package tests, git package
// tests).

// guardingPipe deliberately calls a side-effect helper inside Plan().
// Used to exercise the runtime sentinel via Registration.Invoke (the
// canonical wiring point), since withPlanTime itself is unexported.
type guardingPipe struct{ sparkwing.Base }

func (guardingPipe) Plan(ctx context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.GuardPlanTime(ctx, "test.helper")
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
	// Plain ctx (no Plan() in flight). GuardPlanTime should be a no-op.
	sparkwing.GuardPlanTime(context.Background(), "test.helper")
}

func TestPlanTime_IsPlanTimeFalseByDefault(t *testing.T) {
	if sparkwing.IsPlanTime(context.Background()) {
		t.Fatal("bare ctx should not be plan-time")
	}
}
