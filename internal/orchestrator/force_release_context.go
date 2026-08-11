package orchestrator

import "context"

type forceReleaseDisabledKey struct{}

func withForceReleaseDisabled(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceReleaseDisabledKey{}, true)
}

func forceReleaseAllowed(ctx context.Context) bool {
	disabled, _ := ctx.Value(forceReleaseDisabledKey{}).(bool)
	return !disabled
}
