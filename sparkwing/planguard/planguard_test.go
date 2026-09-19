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

func (mintingPipe) Plan(context.Context, *sparkwing.Plan, sparkwing.NoInputs, sparkwing.RunContext) error {
	planguard.Guard(context.Background(), "test.helper") //nolint:contextcheck // minting a context is the case under test.
	return nil
}

type mintingGrantingPipe struct{ sparkwing.Base }

// safety: a freshly built context records nothing about Plan, so the grant on
// it is honored.
func (mintingGrantingPipe) Plan(context.Context, *sparkwing.Plan, sparkwing.NoInputs, sparkwing.RunContext) error {
	planguard.Guard(sparkwing.Grant(context.Background()), "test.helper") //nolint:contextcheck // minting a context is the case under test.
	return nil
}

type grantingPipe struct{ sparkwing.Base }

func (grantingPipe) Plan(ctx context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	planguard.Guard(sparkwing.Grant(ctx), "test.helper")
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs]("planguard-threading",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return threadingPipe{} })
	sparkwing.Register[sparkwing.NoInputs]("planguard-minting",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return mintingPipe{} })
	sparkwing.Register[sparkwing.NoInputs]("planguard-granting",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return grantingPipe{} })
	sparkwing.Register[sparkwing.NoInputs]("planguard-minting-then-granting",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return mintingGrantingPipe{} })
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
	// safety: granting first is what makes these cases prove the seal beats a
	// grant, rather than arriving where none existed.
	if _, err := reg.Invoke(planguard.Grant(context.Background()), nil, sparkwing.RunContext{Pipeline: name}); err != nil {
		t.Fatalf("Invoke(%s): %v; Plan never ran, so this proves nothing", name, err)
	}
	return ""
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

func TestGuard_RefusesAPlanThatMintsItsOwnContext(t *testing.T) {
	msg := invokePlan(t, "planguard-minting")
	if !strings.Contains(msg, "carrying no grant") {
		t.Fatalf("want the no-grant refusal, got: %q", msg)
	}
}

func TestGuard_RefusesAPlanThatGrantsItsHandedContext(t *testing.T) {
	msg := invokePlan(t, "planguard-granting")
	if !strings.Contains(msg, "called inside Pipeline.Plan()") {
		t.Fatalf("Grant lifted the seal; want the purity refusal, got: %q", msg)
	}
}

func TestGuard_RefusesAContextCarryingNoGrant(t *testing.T) {
	if msg, _ := refusal(context.Background()); !strings.Contains(msg, "carrying no grant") {
		t.Fatalf("an ungranted context must be refused, got: %q", msg)
	}
}

func TestGuard_AllowsAGrantedContext(t *testing.T) {
	if msg, panicked := refusal(planguard.Grant(context.Background())); panicked {
		t.Fatalf("a granted context must pass, got a panic: %q", msg)
	}
}

func TestGrant_IsANoOpOnASealedContext(t *testing.T) {
	sealed := planguard.Seal(planguard.Grant(context.Background()))
	for attempt := 1; attempt <= 2; attempt++ {
		sealed = planguard.Grant(sealed)
		msg, _ := refusal(sealed)
		if !strings.Contains(msg, "called inside Pipeline.Plan()") {
			t.Fatalf("Grant #%d lifted the seal, got: %q", attempt, msg)
		}
	}
}

func TestSeal_BeatsAGrantWhicheverOrderTheyArrive(t *testing.T) {
	if msg, _ := refusal(planguard.Seal(context.Background())); !strings.Contains(msg, "called inside Pipeline.Plan()") {
		t.Fatalf("a bare seal must refuse, got: %q", msg)
	}
	if msg, _ := refusal(planguard.Grant(planguard.Seal(planguard.Grant(context.Background())))); !strings.Contains(msg, "called inside Pipeline.Plan()") {
		t.Fatalf("grant, seal, grant must still refuse, got: %q", msg)
	}
}

func TestGuard_AMintedThenGrantedContextIsAllowedEvenInsidePlan(t *testing.T) {
	if msg := invokePlan(t, "planguard-minting-then-granting"); msg != "" {
		t.Fatalf("the linter refuses this shape, the guard does not; closing it here is allowed, but say so: %q", msg)
	}
}
