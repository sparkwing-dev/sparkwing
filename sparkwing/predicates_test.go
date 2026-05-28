package sparkwing

import (
	"testing"
)

// fakePredCtx is a minimal in-memory PredicateContext for the tests
// in this file. Production resolution uses the real chain's context.
type fakePredCtx struct {
	args    map[string]any
	profile string
	isLocal bool
}

func (f fakePredCtx) Arg(name string) (any, bool) {
	v, ok := f.args[name]
	return v, ok
}
func (f fakePredCtx) ProfileName() string  { return f.profile }
func (f fakePredCtx) ProfileIsLocal() bool { return f.isLocal }

func TestArgEq_MatchesResolvedValue(t *testing.T) {
	ctx := fakePredCtx{args: map[string]any{"target": "prod"}}
	if !ArgEq("target", "prod").Eval(ctx) {
		t.Fatal("ArgEq should match identical string")
	}
	if ArgEq("target", "dev").Eval(ctx) {
		t.Fatal("ArgEq should not match different string")
	}
}

func TestArgEq_UnresolvedReturnsFalse(t *testing.T) {
	ctx := fakePredCtx{args: map[string]any{}}
	if ArgEq("missing", "anything").Eval(ctx) {
		t.Fatal("ArgEq on unresolved arg must return false")
	}
}

func TestArgEq_NumericCoercionAcrossKinds(t *testing.T) {
	// User-typed int literals (3) should match args declared as
	// int32/int64 without forcing the predicate caller to know the
	// exact struct field type.
	cases := []struct {
		name string
		v    any
	}{
		{"int", int(3)},
		{"int32", int32(3)},
		{"int64", int64(3)},
		{"uint", uint(3)},
		{"uint64", uint64(3)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := fakePredCtx{args: map[string]any{"n": c.v}}
			if !ArgEq("n", 3).Eval(ctx) {
				t.Fatalf("ArgEq(n, 3) should match %s value 3", c.name)
			}
		})
	}
}

func TestArgNeq_DistinguishesUnresolvedFromMismatch(t *testing.T) {
	got := ArgNeq("missing", "x").Eval(fakePredCtx{args: map[string]any{}})
	if got {
		t.Fatal("ArgNeq on unresolved arg must return false (use ArgUnset for that case)")
	}
	if !ArgNeq("target", "dev").Eval(fakePredCtx{args: map[string]any{"target": "prod"}}) {
		t.Fatal("ArgNeq should hold when resolved value differs")
	}
	if ArgNeq("target", "prod").Eval(fakePredCtx{args: map[string]any{"target": "prod"}}) {
		t.Fatal("ArgNeq should not hold when resolved value matches")
	}
}

func TestArgIn_MultipleCandidates(t *testing.T) {
	ctx := fakePredCtx{args: map[string]any{"target": "staging"}}
	if !ArgIn("target", "staging", "prod").Eval(ctx) {
		t.Fatal("ArgIn should match staging")
	}
	if ArgIn("target", "dev").Eval(ctx) {
		t.Fatal("ArgIn should not match when value absent from set")
	}
}

func TestArgIn_PanicsOnEmptySet(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ArgIn with no values must panic at construction")
		}
	}()
	_ = ArgIn("target")
}

func TestArgSet_TrueWhenResolved_FalseOtherwise(t *testing.T) {
	if !ArgSet("x").Eval(fakePredCtx{args: map[string]any{"x": 0}}) {
		t.Fatal("ArgSet should hold even when value is zero -- presence, not non-zero-ness")
	}
	if ArgSet("missing").Eval(fakePredCtx{args: map[string]any{}}) {
		t.Fatal("ArgSet should not hold for unresolved arg")
	}
}

func TestArgUnset_InverseOfArgSet(t *testing.T) {
	if ArgUnset("x").Eval(fakePredCtx{args: map[string]any{"x": ""}}) {
		t.Fatal("ArgUnset should not hold when arg has a (zero) resolved value")
	}
	if !ArgUnset("missing").Eval(fakePredCtx{args: map[string]any{}}) {
		t.Fatal("ArgUnset should hold for unresolved arg")
	}
}

func TestAnd_ShortCircuitsOnFirstFalse(t *testing.T) {
	ctx := fakePredCtx{args: map[string]any{"a": 1, "b": 2}}
	if !And(ArgEq("a", 1), ArgEq("b", 2)).Eval(ctx) {
		t.Fatal("And of two true predicates should be true")
	}
	if And(ArgEq("a", 1), ArgEq("b", 99)).Eval(ctx) {
		t.Fatal("And with any false leg should be false")
	}
	if !And().Eval(ctx) {
		t.Fatal("vacuous And() should be true")
	}
}

func TestOr_ShortCircuitsOnFirstTrue(t *testing.T) {
	ctx := fakePredCtx{args: map[string]any{"a": 1}}
	if !Or(ArgEq("a", 1), ArgEq("a", 99)).Eval(ctx) {
		t.Fatal("Or with any true leg should be true")
	}
	if Or(ArgEq("a", 0), ArgEq("a", 99)).Eval(ctx) {
		t.Fatal("Or of all-false predicates should be false")
	}
	if Or().Eval(ctx) {
		t.Fatal("vacuous Or() should be false")
	}
}

func TestNot_Inverts(t *testing.T) {
	ctx := fakePredCtx{args: map[string]any{"a": 1}}
	if Not(ArgEq("a", 1)).Eval(ctx) {
		t.Fatal("Not of true predicate should be false")
	}
	if !Not(ArgEq("a", 99)).Eval(ctx) {
		t.Fatal("Not of false predicate should be true")
	}
}

func TestLocalRemote_PivotOnProfileIsLocal(t *testing.T) {
	local := fakePredCtx{isLocal: true}
	remote := fakePredCtx{isLocal: false}
	if !Local.Eval(local) || Local.Eval(remote) {
		t.Fatal("Local should hold iff ProfileIsLocal is true")
	}
	if Remote.Eval(local) || !Remote.Eval(remote) {
		t.Fatal("Remote should hold iff ProfileIsLocal is false")
	}
}

func TestProfile_NameMatch(t *testing.T) {
	ctx := fakePredCtx{profile: "ci"}
	if !Profile("ci").Eval(ctx) {
		t.Fatal("Profile(ci) should match a ci-resolved context")
	}
	if Profile("prod").Eval(ctx) {
		t.Fatal("Profile(prod) should not match a ci-resolved context")
	}
}

func TestAlways_HoldsRegardlessOfContext(t *testing.T) {
	if !Always().Eval(fakePredCtx{}) {
		t.Fatal("Always() must always be true")
	}
}

func TestCompose_RealWorldShape(t *testing.T) {
	// "required when target=prod AND no image is set"
	ctx := fakePredCtx{args: map[string]any{"target": "prod"}}
	pred := And(ArgEq("target", "prod"), ArgUnset("image"))
	if !pred.Eval(ctx) {
		t.Fatal("compound predicate should hold when target=prod and image unset")
	}
	ctx2 := fakePredCtx{args: map[string]any{"target": "prod", "image": "foo"}}
	if pred.Eval(ctx2) {
		t.Fatal("compound predicate should not hold once image is set")
	}
}

func TestString_RendersReadable(t *testing.T) {
	cases := []struct {
		pred Predicate
		want string
	}{
		{ArgEq("a", 1), "a==1"},
		{ArgNeq("a", 1), "a!=1"},
		{ArgIn("a", 1, 2, 3), "a in [1,2,3]"},
		{ArgSet("a"), "a is set"},
		{ArgUnset("a"), "a is unset"},
		{And(ArgEq("a", 1), ArgUnset("b")), "(a==1 AND b is unset)"},
		{Or(ArgEq("a", 1), ArgEq("b", 2)), "(a==1 OR b==2)"},
		{Not(ArgEq("a", 1)), "NOT a==1"},
		{Local, "profile is local"},
		{Remote, "profile is remote"},
		{Profile("ci"), "profile==ci"},
		{Always(), "always"},
		{And(), "(true)"},
		{Or(), "(false)"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := c.pred.String(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
