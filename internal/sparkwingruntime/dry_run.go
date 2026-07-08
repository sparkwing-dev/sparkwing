package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithDryRun marks ctx so RunWork dispatches each step's DryRunFn
// (or its apply Fn when the step is explicitly marked
// SafeWithoutDryRun) instead of the apply Fn. The sparkwing-level
// `--dry-run` flag installs this on the run-wide ctx so every Work
// executed under it goes through the no-mutation path.
//
// Steps that declare neither DryRunFn nor SafeWithoutDryRun emit a
// `step_skipped` event with reason `no_dry_run_defined` so the
// operator's run logs make the contract gap visible.
func WithDryRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.DryRun, true)
}
