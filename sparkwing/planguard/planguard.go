// Package planguard decides where a side-effect helper may run.
//
// Pipeline.Plan must be pure-declarative; side effects belong inside
// the step closures a Job's Work() body declares; those receive a
// granted context.
//
// A context carries one of three states, and a context with no grant
// refuses. The orchestrator calls Grant once per process that executes
// pipeline work, and Invoke calls Seal for the Plan body.
package planguard

import (
	"context"
	"fmt"
)

type state uint8

const (
	ungranted state = iota
	granted
	sealed
)

type stateKey struct{}

func from(ctx context.Context) state {
	if ctx == nil {
		return ungranted
	}
	s, _ := ctx.Value(stateKey{}).(state)
	return s
}

// Grant returns ctx with side-effect helpers granted, and returns a
// sealed ctx unchanged. The orchestrator calls it once at the root of
// each process that executes pipeline work; everything dispatched
// below inherits the grant.
//
// This is the spelling for the SDK's own layers and for helper
// packages that cannot import the root package. Pipeline authors call
// sparkwing.Grant, which delegates here.
func Grant(ctx context.Context) context.Context {
	if from(ctx) == sealed {
		return ctx
	}
	return context.WithValue(ctx, stateKey{}, granted)
}

// Seal returns ctx with side-effect helpers refused, whatever ctx
// carried before.
func Seal(ctx context.Context) context.Context {
	return context.WithValue(ctx, stateKey{}, sealed)
}

// Guard panics unless ctx grants side effects. `helper` names the
// call that triggered the guard (e.g. "sparkwing.Bash") so the panic
// message tells the author which one to lift into a Job.
func Guard(ctx context.Context, helper string) {
	switch from(ctx) {
	case granted:
		return
	case sealed:
		panic(fmt.Sprintf(
			"sparkwing: %s called inside Pipeline.Plan(). Move side effects into a Job's "+
				"Work() body and surface the result via sparkwing.Step + Ref[T]. "+
				"See docs/sdk.md#plan-must-be-pure",
			helper,
		))
	default:
		panic(fmt.Sprintf(
			"sparkwing: %s called with a context carrying no grant. Thread the context "+
				"your callback was handed. If this call really is outside a run, pass "+
				"sparkwing.Grant(ctx). See docs/sdk.md#side-effects-need-a-grant",
			helper,
		))
	}
}
