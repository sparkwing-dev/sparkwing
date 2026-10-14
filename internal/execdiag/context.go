package execdiag

import (
	"context"
	"time"
)

type policyKey struct{}

type Policy struct {
	Expired         func() bool
	EscalationLimit time.Duration
	OutputLimit     int
}

func FromContext(ctx context.Context) (Policy, bool) {
	policy, ok := ctx.Value(policyKey{}).(Policy)
	return policy, ok
}
