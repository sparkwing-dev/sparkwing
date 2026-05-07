package sparkwing_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

type deployArgs struct {
	Service string
	Env     string
}

// SDK-041: sw.Inputs[T](ctx) round-trips the typed value the
// orchestrator (or a test) installs via sw.WithInputs.
func TestInputs_RoundTrip(t *testing.T) {
	want := deployArgs{Service: "api", Env: "prod"}
	ctx := sparkwing.WithInputs(context.Background(), want)
	got := sparkwing.Inputs[deployArgs](ctx)
	if got != want {
		t.Fatalf("Inputs[deployArgs] = %+v, want %+v", got, want)
	}
}

// Calling Inputs[T] without an installer panics with a message
// naming the expected type and pointing at the orchestrator
// boundary -- matches how Secret / Config behave when called outside
// a dispatch ctx.
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

// Asking for the wrong concrete type is a programmer mistake;
// panic loud rather than zero-value silently.
func TestInputs_PanicsOnTypeMismatch(t *testing.T) {
	type otherArgs struct {
		Region string
	}
	ctx := sparkwing.WithInputs(context.Background(), deployArgs{Service: "x"})
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

// Plan.Inputs returns the value the registration parsed; same value
// the orchestrator hands to WithInputs at dispatch time. This pins
// the wiring contract without spinning up an orchestrator run.
func TestPlanInputs_RoundTrip(t *testing.T) {
	plan := sparkwing.NewPlan()
	if got := plan.Inputs(); got != nil {
		t.Fatalf("fresh Plan.Inputs() = %+v, want nil", got)
	}
}
