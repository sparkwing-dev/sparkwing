package sparkwing

import (
	"context"
	"fmt"
)

// Inputs returns the typed Inputs struct that the orchestrator
// parsed for the current run -- the same value the pipeline's
// Plan(ctx, plan, in T, rc) method received. Available from any
// step body so authors don't have to thread inputs through closures
// or job-struct fields.
//
//	type DeployArgs struct {
//	    Service string `flag:"service"`
//	    Env     string `flag:"env" default:"staging"`
//	}
//
//	func (Deploy) Plan(ctx context.Context, plan *sw.Plan, _ DeployArgs, rc sw.RunContext) error {
//	    sw.Job(plan, "deploy", func(ctx context.Context) error {
//	        args := sw.Inputs[DeployArgs](ctx)
//	        return runDeploy(ctx, args.Service, args.Env)
//	    })
//	    return nil
//	}
//
// Panics when no inputs are installed (called outside the
// orchestrator's dispatch ctx) or when the installed inputs aren't
// assignable to T (programmer mistake -- requested the wrong
// pipeline's Inputs type, e.g. from a SpawnNode'd job whose
// Workable doesn't share the parent's Inputs shape).
func Inputs[T any](ctx context.Context) T {
	raw := ctx.Value(keyInputs)
	if raw == nil {
		var zero T
		panic(fmt.Sprintf("sparkwing: Inputs[%T]: no inputs installed in ctx (called outside the orchestrator?)", zero))
	}
	v, ok := raw.(T)
	if !ok {
		var zero T
		panic(fmt.Sprintf("sparkwing: Inputs[%T]: installed inputs are %T, not assignable", zero, raw))
	}
	return v
}

// WithInputs installs the parsed Inputs struct on ctx. Intended for
// orchestrator implementations; pipeline authors don't construct
// dispatch ctx directly. The orchestrator passes the Plan's stored
// inputs (set by the registration's invoke wrapper) so every step
// body sees the typed value the Plan() method received.
func WithInputs(ctx context.Context, in any) context.Context {
	return context.WithValue(ctx, keyInputs, in)
}
