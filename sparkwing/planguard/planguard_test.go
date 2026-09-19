package planguard_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

func refusal(ctx context.Context) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg, _ = r.(string)
		}
	}()
	planguard.Guard(ctx, "test.helper")
	return ""
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

type mintingDetachingPipe struct{ sparkwing.Base }

// safety: a freshly built context records nothing about Plan, so the grant on
// it is honored.
func (mintingDetachingPipe) Plan(context.Context, *sparkwing.Plan, sparkwing.NoInputs, sparkwing.RunContext) error {
	planguard.Guard(sparkwing.Grant(context.Background()), "test.helper") //nolint:contextcheck // minting a context is the case under test.
	return nil
}

type detachingPipe struct{ sparkwing.Base }

func (detachingPipe) Plan(ctx context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	planguard.Guard(sparkwing.Grant(ctx), "test.helper")
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs]("planguard-threading",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return threadingPipe{} })
	sparkwing.Register[sparkwing.NoInputs]("planguard-minting",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return mintingPipe{} })
	sparkwing.Register[sparkwing.NoInputs]("planguard-detaching",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return detachingPipe{} })
	sparkwing.Register[sparkwing.NoInputs]("planguard-minting-then-detaching",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return mintingDetachingPipe{} })
}

func invokePlan(t *testing.T, name string) (msg string) {
	t.Helper()
	reg, ok := sparkwing.Lookup(name)
	if !ok {
		t.Fatalf("pipeline %q did not register", name)
	}
	defer func() {
		if r := recover(); r != nil {
			msg, _ = r.(string)
		}
	}()
	// safety: granting first is what makes these cases prove the seal beats a
	// grant, rather than arriving where none existed.
	_, _ = reg.Invoke(planguard.Grant(context.Background()), nil, sparkwing.RunContext{Pipeline: name})
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

func TestGuard_RefusesAPlanThatDetachesItsContext(t *testing.T) {
	msg := invokePlan(t, "planguard-detaching")
	if !strings.Contains(msg, "called inside Pipeline.Plan()") {
		t.Fatalf("Detached lifted the seal; want the purity refusal, got: %q", msg)
	}
}

func TestGuard_RefusesAContextCarryingNoGrant(t *testing.T) {
	if msg := refusal(context.Background()); !strings.Contains(msg, "carrying no grant") {
		t.Fatalf("an ungranted context must be refused, got: %q", msg)
	}
}

func TestGuard_AllowsAGrantedContext(t *testing.T) {
	if msg := refusal(planguard.Grant(context.Background())); msg != "" {
		t.Fatalf("a granted context must pass, got: %q", msg)
	}
}

func TestAllow_IsANoOpOnASealedContext(t *testing.T) {
	sealed := planguard.Seal(planguard.Grant(context.Background()))
	for _, name := range []string{"once", "twice"} {
		sealed = planguard.Grant(sealed)
		if msg := refusal(sealed); !strings.Contains(msg, "called inside Pipeline.Plan()") {
			t.Fatalf("Allow(%s) lifted the seal, got: %q", name, msg)
		}
	}
}

func TestSeal_BeatsAGrantWhicheverOrderTheyArrive(t *testing.T) {
	if msg := refusal(planguard.Seal(context.Background())); !strings.Contains(msg, "called inside Pipeline.Plan()") {
		t.Fatalf("a bare seal must refuse, got: %q", msg)
	}
	if msg := refusal(planguard.Grant(planguard.Seal(planguard.Grant(context.Background())))); !strings.Contains(msg, "called inside Pipeline.Plan()") {
		t.Fatalf("grant, seal, grant must still refuse, got: %q", msg)
	}
}

func TestGuard_AMintedThenDetachedContextIsAllowedEvenInsidePlan(t *testing.T) {
	if msg := invokePlan(t, "planguard-minting-then-detaching"); msg != "" {
		t.Fatalf("this evasion is expected to pass the guard today, got a refusal: %q", msg)
	}
}
