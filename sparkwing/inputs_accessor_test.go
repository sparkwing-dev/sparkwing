package sparkwing_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type deployArgs struct {
	Service string
	Env     string
}

func TestInputs_RoundTrip(t *testing.T) {
	want := deployArgs{Service: "api", Env: "prod"}
	ctx := sparkwingruntime.WithInputs(context.Background(), want)
	got := sparkwing.Inputs[deployArgs](ctx)
	if got != want {
		t.Fatalf("Inputs[deployArgs] = %+v, want %+v", got, want)
	}
}

func TestInputs_PanicsWithoutInstaller(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on missing installer")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Inputs[") || !strings.Contains(msg, "no inputs installed") {
			t.Fatalf("panic should mention Inputs and missing installer, got %q", msg)
		}
	}()
	_ = sparkwing.Inputs[deployArgs](context.Background())
}

func TestInputs_PanicsOnTypeMismatch(t *testing.T) {
	type otherArgs struct {
		Region string
	}
	ctx := sparkwingruntime.WithInputs(context.Background(), deployArgs{Service: "x"})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on type mismatch")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "not assignable") {
			t.Fatalf("panic should say not assignable, got %q", msg)
		}
	}()
	_ = sparkwing.Inputs[otherArgs](ctx)
}

func TestPlanInputs_RoundTrip(t *testing.T) {
	plan := sparkwing.NewPlan()
	if got := plan.Inputs(); got != nil {
		t.Fatalf("fresh Plan.Inputs() = %+v, want nil", got)
	}
}
