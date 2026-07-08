package sparkwing

import (
	"context"
	"errors"
	"fmt"
)

// resolveAndBindJobArgs walks every job in the plan that registered
// a typed args [Schema] via [WithArgs[T]], resolves each against the
// supplied flag values + profile defaults, and binds the resolved
// typed args back onto the job's WithArgs holder so step bodies
// reading j.Args(ctx) see the populated values.
//
// Also returns the merged resolved-args map across all jobs (keyed
// by flag name) so the caller can install it on the run context for
// later sparkwing.Arg[T](ctx, name) reads. Same-flag-name across two
// jobs is impossible at this point -- [registerJobArgs] rejected
// collisions at plan time -- so the merge is conflict-free by
// construction.
//
// Returns the merged error of every per-job resolution failure
// joined together so callers see every missing required arg / failed
// predicate / group violation in one error rather than one at a time.
func resolveAndBindJobArgs(plan *Plan, in ResolveInputs) (map[string]any, error) {
	if plan == nil {
		return nil, nil
	}
	schemas := plan.JobArgSchemas()
	if len(schemas) == 0 {
		return nil, nil
	}
	merged := make(map[string]any, 16)
	var problems []error

	for jobID, schema := range schemas {
		argsValue, err := schema.Resolve(in)
		if err != nil {
			problems = append(problems, fmt.Errorf("job %q: %w", jobID, err))
			continue
		}
		node := plan.Job(jobID)
		if node == nil {
			problems = append(problems, fmt.Errorf("job %q: registered args but plan has no node by that id", jobID))
			continue
		}
		holder, _ := embeddedArgs(node.Job())
		if holder == nil {
			problems = append(problems, fmt.Errorf("job %q: registered args but Workable has no WithArgs holder", jobID))
			continue
		}
		if err := holder.BindFromAny(argsValue.Interface()); err != nil {
			problems = append(problems, fmt.Errorf("job %q: bind resolved args: %w", jobID, err))
			continue
		}
		for _, m := range schema.fields {
			if m.Flag == "" {
				continue
			}
			f := argsValue.FieldByName(m.Name)
			if !f.IsValid() {
				continue
			}
			merged[m.Flag] = f.Interface()
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return merged, nil
}

// WithResolvedArgs installs a resolved-args map on the context so
// sparkwing.Arg[T] / ArgOrDefault can read it from any step body.
// Called by the framework's dispatch path after [resolveAndBindJobArgs]
// produces the merged map. Public so internal/orchestrator can call
// it without going through the RuntimePlumbing keys directly.
func WithResolvedArgs(ctx context.Context, args map[string]any) context.Context {
	if args == nil {
		return ctx
	}
	return context.WithValue(ctx, keyResolvedArgs, args)
}
