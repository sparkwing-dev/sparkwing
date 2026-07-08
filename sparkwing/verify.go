package sparkwing

import "context"

// FailureStage identifies which lifecycle stage produced a node failure.
// It is carried on [Failure] so a failure-aware OnFailure recovery can
// distinguish "the action failed" from "the action succeeded but its
// Verify postcondition failed."
type FailureStage int

const (
	// StageAction marks a failure in the node's action: its Run exited
	// non-zero, panicked, or hit its Timeout.
	StageAction FailureStage = iota
	// StageVerify marks a failure in the node's Verify postcondition: the
	// action completed, but the check returned a non-nil error.
	StageVerify
)

// String returns "action" or "verify".
func (s FailureStage) String() string {
	if s == StageVerify {
		return "verify"
	}
	return "action"
}

// Failure describes why a node terminated unsuccessfully. It is passed
// to a failure-aware OnFailure recovery callback (a
// func(ctx, Failure) error) so recovery logic can branch on the stage at
// which the node failed.
//
// The two stages call for different recovery. An action failure may have
// left state half-applied -- converging forward (re-running the action)
// is usually safer than blindly rolling back. A verify failure means the
// action ran to completion, so the prior artifact is a clean rollback
// target. Whether a verify failure is a definitive "unhealthy" or merely
// "could not check" is deliberately not modeled here; that distinction
// lives in the check's error value (for example a probe library's
// Indeterminate helper), which [Failure.Err] carries.
type Failure struct {
	// Stage is the lifecycle stage that produced the failure.
	Stage FailureStage
	// Err is the underlying error: the action error for StageAction, or
	// the verification error for StageVerify.
	Err error
}

// VerifyFn is a postcondition checked after a node's action succeeds. A
// non-nil return fails the node at [StageVerify].
type VerifyFn func(ctx context.Context) error

// FailureRecoveryFn is the failure-aware recovery shape [JobNode.OnFailure]
// accepts in addition to a [Workable] or a func(ctx) error. It receives
// the [Failure] describing how the node terminated, so recovery can
// branch on f.Stage and inspect f.Err.
type FailureRecoveryFn func(ctx context.Context, f Failure) error

// VerifyError wraps the error returned by a node's Verify check. The
// orchestrator uses it to attribute the failure to [StageVerify]; it
// unwraps to the check's original error.
type VerifyError struct{ Err error }

func (e *VerifyError) Error() string { return "verify: " + e.Err.Error() }
func (e *VerifyError) Unwrap() error { return e.Err }

type failureCtxKey struct{}

// WithFailure returns a context carrying f, read back by a failure-aware
// recovery callback via [FailureFromContext]. The orchestrator installs
// it on the context handed to an OnFailure recovery node.
func WithFailure(ctx context.Context, f Failure) context.Context {
	return context.WithValue(ctx, failureCtxKey{}, f)
}

// FailureFromContext returns the [Failure] installed by [WithFailure], or
// the zero Failure (StageAction, nil Err) when none is present.
func FailureFromContext(ctx context.Context) Failure {
	if f, ok := ctx.Value(failureCtxKey{}).(Failure); ok {
		return f
	}
	return Failure{}
}

// recoveryFn is the unexported Workable wrapper installed when OnFailure
// receives a [FailureRecoveryFn]. At run time it reads the parent's
// Failure from context and invokes the callback.
type recoveryFn struct {
	fn FailureRecoveryFn
}

func (c *recoveryFn) Work(w *Work) (*WorkStep, error) {
	Step(w, "run", func(ctx context.Context) error {
		return c.fn(ctx, FailureFromContext(ctx))
	})
	return nil, nil
}
