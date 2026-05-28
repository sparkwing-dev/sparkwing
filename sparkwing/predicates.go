package sparkwing

import (
	"fmt"
	"reflect"
	"strings"
)

// Predicate is the closed condition language used by the args
// resolution chain: RequiredWhen, group .When(), default-when, etc.
// Implementations are sparkwing-defined (ArgEq, ArgIn, ArgSet,
// ArgUnset, And, Or, Not, Local, Remote, Profile, Always); the
// isPredicate marker method keeps user code from inventing its own
// (use [Custom] for arbitrary validators instead -- it sits on the
// constraint side, not the predicate side, so the resolution chain
// can keep its termination guarantees).
//
// Predicates are evaluated against a [PredicateContext] that exposes
// already-resolved arg values plus profile metadata. A predicate that
// references an unresolved arg via [ArgEq]/[ArgIn] evaluates false
// for the comparison legs and true for [ArgUnset]; this is intentional
// so that "required when X is unset" works even before X's resolution
// step has run, and "required when X equals Y" never accidentally
// fires against a still-zero value.
type Predicate interface {
	// Eval returns true when the condition holds against the current
	// resolution context.
	Eval(ctx PredicateContext) bool
	// String returns a deterministic human-readable rendering for
	// error messages and the `pipeline describe --args` view. Avoid
	// embedding values that change run-to-run.
	String() string

	isPredicate()
}

// PredicateContext is the evaluation environment for a [Predicate].
// Implemented by the resolution chain; users never construct one
// directly outside of tests (where a minimal in-memory impl is fine).
type PredicateContext interface {
	// Arg returns the resolved value of the named arg and true when
	// some source provided a value (explicit flag, profile
	// default-args, computed, or a constraint Default). Returns
	// (nil, false) when the arg has no resolved value at all -- this
	// is the signal [ArgUnset] looks for.
	Arg(name string) (value any, ok bool)
	// ProfileName is the resolved profile's name.
	ProfileName() string
	// ProfileIsLocal returns true when the resolved profile has no
	// controller -- the run executes on the operator's machine. Used
	// by [Local] / [Remote].
	ProfileIsLocal() bool
}

// --- value comparison predicates ---

type argEqPredicate struct {
	name  string
	value any
}

func (p argEqPredicate) Eval(ctx PredicateContext) bool {
	v, ok := ctx.Arg(p.name)
	if !ok {
		return false
	}
	return predicateValueEqual(v, p.value)
}

func (p argEqPredicate) String() string { return fmt.Sprintf("%s==%v", p.name, p.value) }
func (argEqPredicate) isPredicate()     {}

// ArgEq holds when the named arg's resolved value equals value.
// Comparison uses [reflect.DeepEqual] with numeric-type coercion so
// ArgEq("replicas", 3) matches whether the arg is declared as int,
// int32, or int64.
func ArgEq(name string, value any) Predicate {
	return argEqPredicate{name: name, value: value}
}

type argNeqPredicate struct {
	name  string
	value any
}

func (p argNeqPredicate) Eval(ctx PredicateContext) bool {
	v, ok := ctx.Arg(p.name)
	if !ok {
		// Unresolved args don't satisfy "not equal X" -- treating an
		// unresolved value as "different from anything" makes
		// required-when predicates fire surprisingly often. Use
		// ArgUnset explicitly when that's the intent.
		return false
	}
	return !predicateValueEqual(v, p.value)
}

func (p argNeqPredicate) String() string { return fmt.Sprintf("%s!=%v", p.name, p.value) }
func (argNeqPredicate) isPredicate()     {}

// ArgNeq holds when the named arg has a resolved value AND that value
// is not equal to value. Unresolved args do NOT satisfy ArgNeq -- use
// ArgUnset for that case.
func ArgNeq(name string, value any) Predicate {
	return argNeqPredicate{name: name, value: value}
}

type argInPredicate struct {
	name   string
	values []any
}

func (p argInPredicate) Eval(ctx PredicateContext) bool {
	v, ok := ctx.Arg(p.name)
	if !ok {
		return false
	}
	for _, candidate := range p.values {
		if predicateValueEqual(v, candidate) {
			return true
		}
	}
	return false
}

func (p argInPredicate) String() string {
	parts := make([]string, len(p.values))
	for i, v := range p.values {
		parts[i] = fmt.Sprintf("%v", v)
	}
	return fmt.Sprintf("%s in [%s]", p.name, strings.Join(parts, ","))
}

func (argInPredicate) isPredicate() {}

// ArgIn holds when the named arg's resolved value matches any of the
// supplied values. Empty values panics at construction (an empty set
// can never match and almost always indicates a bug).
func ArgIn(name string, values ...any) Predicate {
	if len(values) == 0 {
		panic(fmt.Sprintf("sparkwing.ArgIn(%q): values must be non-empty", name))
	}
	return argInPredicate{name: name, values: values}
}

// --- presence predicates ---

type argSetPredicate struct{ name string }

func (p argSetPredicate) Eval(ctx PredicateContext) bool {
	_, ok := ctx.Arg(p.name)
	return ok
}

func (p argSetPredicate) String() string { return fmt.Sprintf("%s is set", p.name) }
func (argSetPredicate) isPredicate()     {}

// ArgSet holds when some source (explicit flag, profile default-args,
// schema default, or computed) has populated the named arg. Note this
// is "has a resolved value," not "has a non-zero value" -- a default of
// false on a bool still counts as set.
func ArgSet(name string) Predicate { return argSetPredicate{name: name} }

type argUnsetPredicate struct{ name string }

func (p argUnsetPredicate) Eval(ctx PredicateContext) bool {
	_, ok := ctx.Arg(p.name)
	return !ok
}

func (p argUnsetPredicate) String() string { return fmt.Sprintf("%s is unset", p.name) }
func (argUnsetPredicate) isPredicate()     {}

// ArgUnset holds when no source has populated the named arg -- it has
// no resolved value at all. Useful for "required when no image is
// given" style fallbacks.
func ArgUnset(name string) Predicate { return argUnsetPredicate{name: name} }

// --- combinators ---

type andPredicate struct{ preds []Predicate }

func (p andPredicate) Eval(ctx PredicateContext) bool {
	for _, sub := range p.preds {
		if !sub.Eval(ctx) {
			return false
		}
	}
	return true
}

func (p andPredicate) String() string {
	if len(p.preds) == 0 {
		return "(true)"
	}
	parts := make([]string, len(p.preds))
	for i, sub := range p.preds {
		parts[i] = sub.String()
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}

func (andPredicate) isPredicate() {}

// And holds when every nested predicate holds. Vacuous when called
// with no arguments (returns a predicate that always evaluates true);
// guard with an explicit Always() if the empty case is intentional.
func And(preds ...Predicate) Predicate { return andPredicate{preds: preds} }

type orPredicate struct{ preds []Predicate }

func (p orPredicate) Eval(ctx PredicateContext) bool {
	for _, sub := range p.preds {
		if sub.Eval(ctx) {
			return true
		}
	}
	return false
}

func (p orPredicate) String() string {
	if len(p.preds) == 0 {
		return "(false)"
	}
	parts := make([]string, len(p.preds))
	for i, sub := range p.preds {
		parts[i] = sub.String()
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

func (orPredicate) isPredicate() {}

// Or holds when any nested predicate holds. Vacuous when called with
// no arguments (returns a predicate that always evaluates false).
func Or(preds ...Predicate) Predicate { return orPredicate{preds: preds} }

type notPredicate struct{ inner Predicate }

func (p notPredicate) Eval(ctx PredicateContext) bool { return !p.inner.Eval(ctx) }
func (p notPredicate) String() string                 { return "NOT " + p.inner.String() }
func (notPredicate) isPredicate()                     {}

// Not inverts the nested predicate. Composes with And/Or so callers
// can build small boolean expressions over the resolved args.
func Not(p Predicate) Predicate { return notPredicate{inner: p} }

// --- context predicates ---

type localPredicate struct{}

func (localPredicate) Eval(ctx PredicateContext) bool { return ctx.ProfileIsLocal() }
func (localPredicate) String() string                 { return "profile is local" }
func (localPredicate) isPredicate()                   {}

// Local holds when the resolved profile has no controller -- the run
// executes on the operator's machine rather than being submitted to a
// remote dispatcher. The mirror predicate is [Remote].
var Local Predicate = localPredicate{}

type remotePredicate struct{}

func (remotePredicate) Eval(ctx PredicateContext) bool { return !ctx.ProfileIsLocal() }
func (remotePredicate) String() string                 { return "profile is remote" }
func (remotePredicate) isPredicate()                   {}

// Remote holds when the resolved profile has a controller -- the run
// dispatches to a remote runner. Mirror of [Local].
var Remote Predicate = remotePredicate{}

type profilePredicate struct{ name string }

func (p profilePredicate) Eval(ctx PredicateContext) bool { return ctx.ProfileName() == p.name }
func (p profilePredicate) String() string                 { return fmt.Sprintf("profile==%s", p.name) }
func (profilePredicate) isPredicate()                     {}

// Profile holds when the named profile is the resolved profile. Use
// for environment-specific requirements that don't generalize to
// Local/Remote ("required when profile is ci, regardless of whether
// ci has a controller").
func Profile(name string) Predicate { return profilePredicate{name: name} }

type alwaysPredicate struct{}

func (alwaysPredicate) Eval(PredicateContext) bool { return true }
func (alwaysPredicate) String() string             { return "always" }
func (alwaysPredicate) isPredicate()               {}

// Always holds unconditionally. Equivalent to [FieldBuilder.Required]
// but composable into Or expressions for "required when X or always
// in some fallback profile" patterns.
func Always() Predicate { return alwaysPredicate{} }

// --- helpers ---

// predicateValueEqual compares two values for predicate purposes.
// Uses reflect.DeepEqual but normalizes numeric types so user-typed
// int literals (3) match args declared as int32/int64/uint, and float
// literals match across float32/float64. Strings, bools, and arbitrary
// structs fall through to DeepEqual.
func predicateValueEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == b
	}
	if reflect.DeepEqual(a, b) {
		return true
	}
	av := reflect.ValueOf(a)
	bv := reflect.ValueOf(b)
	if av.Kind() == reflect.Ptr {
		if av.IsNil() {
			return false
		}
		av = av.Elem()
	}
	if bv.Kind() == reflect.Ptr {
		if bv.IsNil() {
			return false
		}
		bv = bv.Elem()
	}
	if isIntKind(av.Kind()) && isIntKind(bv.Kind()) {
		return av.Int() == bv.Int()
	}
	if isUintKind(av.Kind()) && isUintKind(bv.Kind()) {
		return av.Uint() == bv.Uint()
	}
	if isIntKind(av.Kind()) && isUintKind(bv.Kind()) {
		return av.Int() >= 0 && uint64(av.Int()) == bv.Uint()
	}
	if isUintKind(av.Kind()) && isIntKind(bv.Kind()) {
		return bv.Int() >= 0 && av.Uint() == uint64(bv.Int())
	}
	if isFloatKind(av.Kind()) && isFloatKind(bv.Kind()) {
		return av.Float() == bv.Float()
	}
	return false
}

func isIntKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	}
	return false
}

func isUintKind(k reflect.Kind) bool {
	switch k {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

func isFloatKind(k reflect.Kind) bool {
	return k == reflect.Float32 || k == reflect.Float64
}
