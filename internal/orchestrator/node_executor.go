package orchestrator

import (
	"context"
)

type nodeClaimHolderKey struct{}

func withNodeClaimHolder(ctx context.Context, holderID string) context.Context {
	return context.WithValue(ctx, nodeClaimHolderKey{}, holderID)
}
