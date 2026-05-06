package sparkwing

import "context"

// dryRunKey scopes WithDryRun's context value.
type dryRunKey struct{}

// WithDryRun marks ctx so RunWork dispatches each step's DryRunFn
// (or its apply Fn when the step is explicitly marked
// SafeWithoutDryRun) instead of the apply Fn. IMP-014: the wing-
// level `--dry-run` flag installs this on the run-wide ctx so every
// Work executed under it goes through the no-mutation path.
//
// Steps that declare neither DryRunFn nor SafeWithoutDryRun emit a
// `step_skipped` event with reason `no_dry_run_defined` so the
// operator's run logs make the contract gap visible. (IMP-015 will
// tighten this to a hard refusal at dispatch time when paired with
// blast-radius markers; for now the soft path keeps existing
// pipelines working unmodified.)
func WithDryRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, dryRunKey{}, true)
}

// IsDryRun reports whether ctx is in dry-run mode. Exported so
// authors who hand-write step bodies that need to branch on the
// mode (e.g. emitting structured "would do X" log lines) can read
// the flag without a separate plumbing surface.
func IsDryRun(ctx context.Context) bool {
	v, _ := ctx.Value(dryRunKey{}).(bool)
	return v
}

// DryRun installs a dry-run body on this WorkStep. The closure runs
// in place of the apply Fn whenever the orchestrator dispatches the
// step under WithDryRun(ctx). DryRun bodies must NEVER mutate state
// -- the contract is "what would the apply do, written so it can be
// inspected without doing it" (terraform plan, kubectl apply
// --dry-run=server, helm upgrade --dry-run, etc.).
//
//	sparkwing.Step(w, "apply-eks", j.applyEKS).
//	    DryRun(j.dryApplyEKS)
//
// IMP-014.
func (s *WorkStep) DryRun(fn func(ctx context.Context) error) *WorkStep {
	if fn != nil {
		s.dryRunFn = fn
	}
	return s
}

// SafeWithoutDryRun marks a step as having no side effects, so the
// dispatcher runs the apply Fn directly under --dry-run rather than
// requiring a separate dry-run body. The marker is the explicit
// "I considered the dry-run contract and this step doesn't need
// one" answer; it should NOT be applied to skip the work of writing
// a real dry-run for a step that does mutate.
//
//	sparkwing.Step(w, "read-cluster-state", j.readState).
//	    SafeWithoutDryRun()
//
// IMP-014.
func (s *WorkStep) SafeWithoutDryRun() *WorkStep {
	s.safeWithoutDryRun = true
	return s
}

// HasDryRun reports whether a dry-run body has been installed.
// Used by PreviewPlan to render `would_dry_run` for steps whose
// dispatcher path under --dry-run would call DryRunFn.
func (s *WorkStep) HasDryRun() bool { return s.dryRunFn != nil }

// IsSafeWithoutDryRun reports whether the step is marked safe.
// Mirrors HasDryRun's PreviewPlan use: under --dry-run a safe step
// renders `would_run` (apply Fn is what executes).
func (s *WorkStep) IsSafeWithoutDryRun() bool { return s.safeWithoutDryRun }
