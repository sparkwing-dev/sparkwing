package execdiag

import (
	"context"
	"time"
)

type policyKey struct{}

type Policy struct {
	Expired func() bool
	Grace   time.Duration
}

func WithPolicy(ctx context.Context, policy Policy) context.Context {
	return context.WithValue(ctx, policyKey{}, policy)
}

func FromContext(ctx context.Context) (Policy, bool) {
	policy, ok := ctx.Value(policyKey{}).(Policy)
	return policy, ok
}
