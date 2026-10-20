package lease

import "context"

type executionKey struct{}

// ExecutionContext detaches requested cancellation while retaining the keeper's
// execution bound. SDK cancellation and registered cleanups share that lifetime.
func ExecutionContext(ctx context.Context) context.Context {
	detached := context.WithoutCancel(ctx)
	lifetime, ok := ctx.Value(executionKey{}).(context.Context)
	if !ok {
		return detached
	}
	execution, cancel := context.WithCancelCause(detached)
	context.AfterFunc(lifetime, func() { cancel(context.Cause(lifetime)) })
	if lifetime.Err() != nil {
		cancel(context.Cause(lifetime))
	}
	return execution
}
