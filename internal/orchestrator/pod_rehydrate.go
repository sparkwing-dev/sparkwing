package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func rehydratePipelineSecrets(ctx context.Context, _ []byte, reg *sparkwing.Registration) (any, error) {
	if reg == nil {
		return nil, nil
	}
	return sparkwingruntime.ResolvePipelineSecrets(ctx, reg, nil)
}
