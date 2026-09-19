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

func TestGuard_ReadsEveryStateAContextCanCarry(t *testing.T) {
	const (
		planRefusal  = "called inside Pipeline.Plan()"
		grantRefusal = "carrying no grant"
	)
	bg := func() context.Context { return context.Background() }
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"a fresh context", bg(), grantRefusal},
		{"granted", planguard.Grant(bg()), ""},
		{"granted twice", planguard.Grant(planguard.Grant(bg())), ""},
		{"sealed", planguard.Seal(bg()), planRefusal},
		{"sealed over a grant", planguard.Seal(planguard.Grant(bg())), planRefusal},
		{"granted after sealing", planguard.Grant(planguard.Seal(planguard.Grant(bg()))), planRefusal},
		{"granted twice after sealing", planguard.Grant(planguard.Grant(planguard.Seal(bg()))), planRefusal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, panicked := refusal(tc.ctx)
			if tc.want == "" {
				if panicked {
					t.Fatalf("%s must reach the helper, got: %q", tc.name, msg)
				}
				return
			}
			if !panicked {
				t.Fatalf("%s must be refused, and the helper ran", tc.name)
			}
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("refusal = %q, want it to carry %q", msg, tc.want)
			}
		})
	}
}

func TestGuard_ReadsANilContextAsCarryingNoGrant(t *testing.T) {
	//nolint:staticcheck // a nil context is the case under test
	msg, panicked := refusal(nil)
	if !panicked || !strings.Contains(msg, "carrying no grant") {
		t.Fatalf("Guard(nil) = %q (panicked=%v); nil carries no grant and must be refused by name", msg, panicked)
	}
}

func TestGrantAndSeal_LeaveANilParentToTheStandardLibrary(t *testing.T) {
	for name, derive := range map[string]func(context.Context) context.Context{
		"Grant": planguard.Grant,
		"Seal":  planguard.Seal,
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s(nil) returned a context; deriving one from nil must panic at the caller", name)
				}
			}()
			_ = derive(nil)
		})
	}
}

// safety: the guard does not close this shape; the plan-io lint refuses a
// Grant written inside a Plan body. Closing it here is allowed -- change this
// test deliberately rather than reading a failure as a regression.
func TestGuard_AMintedThenGrantedContextIsAllowedEvenInsidePlan(t *testing.T) {
	if msg := invokePlan(t, "planguard-minting-then-granting"); msg != "" {
		t.Fatalf("a minted, granted context must reach the helper; got a refusal: %q", msg)
	}
}
