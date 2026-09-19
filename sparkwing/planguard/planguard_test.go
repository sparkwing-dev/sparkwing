package planguard_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

func refusal(ctx context.Context) (msg string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			msg = fmt.Sprint(r)
		}
	}()
	planguard.Guard(ctx, "test.helper")
	return "", false
}

type threadingPipe struct{ sparkwing.Base }

func (threadingPipe) Plan(ctx context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	planguard.Guard(ctx, "test.helper")
	return nil
}

type mintingPipe struct{ sparkwing.Base }

// safety: threading the handed context here would delete the case under test.
func (mintingPipe) Plan(context.Context, *sparkwing.Plan, sparkwing.NoInputs, sparkwing.RunContext) error {
	planguard.Guard(context.Background(), "test.helper") //nolint:contextcheck // minting a context is the case under test.
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs]("planguard-threading",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return threadingPipe{} })
	sparkwing.Register[sparkwing.NoInputs]("planguard-minting",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return mintingPipe{} })
}

func invokePlan(t *testing.T, name string) (msg string) {
	t.Helper()
	reg, ok := sparkwing.Lookup(name)
	if !ok {
		t.Fatalf("pipeline %q did not register", name)
	}
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	if _, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: name}); err != nil {
		t.Fatalf("Invoke(%s): %v; Plan never ran, so this proves nothing", name, err)
	}
	return ""
}

func TestGuard_ReadsEveryStateAContextCanCarry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		refuses bool
	}{
		{"a context from outside Plan", context.Background(), false},
		{"a sealed context", planguard.With(context.Background()), true},
		{"a context sealed twice", planguard.With(planguard.With(context.Background())), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, panicked := refusal(tc.ctx)
			if tc.refuses != panicked {
				t.Fatalf("%s: panicked=%v, want %v (got %q)", tc.name, panicked, tc.refuses, msg)
			}
			if panicked && !strings.Contains(msg, "called inside Pipeline.Plan()") {
				t.Fatalf("refusal = %q, want it to name the Plan purity rule", msg)
			}
		})
	}
}

func TestGuard_RefusesAPlanThatThreadsItsContext(t *testing.T) {
	msg := invokePlan(t, "planguard-threading")
	if !strings.Contains(msg, "called inside Pipeline.Plan()") {
		t.Fatalf("want the purity refusal, got: %q", msg)
	}
	if !strings.Contains(msg, "test.helper") {
		t.Fatalf("the refusal should name the helper, got: %q", msg)
	}
}

// safety: the seal travels on the context, so a Plan body that mints a fresh
// one escapes it. `sparkwing pipeline lint` is what refuses that shape --
// closing it here is allowed, but change this test deliberately.
func TestGuard_AMintedContextEscapesTheSeal(t *testing.T) {
	if msg := invokePlan(t, "planguard-minting"); msg != "" {
		t.Fatalf("a minted context must reach the helper; got a refusal: %q", msg)
	}
}
