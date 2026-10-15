package nodehost

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func (g *grant) output(ctx context.Context, job string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, localServiceTimeout)
	defer cancel()
	if !validNodeID(job) {
		return nil, executorError(apierr.BadRequest, "invalid job id")
	}
	if job == g.NodeID && g.Output != nil {
		return g.Output, nil
	}
	if g.Local == nil {
		return nil, serviceUnavailable()
	}
	return g.localOutput(ctx, job)
}

func (g *grant) latest(ctx context.Context, pipeline, job string, age time.Duration) (ProcessOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, localServiceTimeout)
	defer cancel()
	if !validName(pipeline) || !validNodeID(job) {
		return ProcessOutput{}, executorError(apierr.BadRequest, "invalid pipeline or job")
	}
	if g.Local == nil {
		return ProcessOutput{}, serviceUnavailable()
	}
	return g.localLatest(ctx, pipeline, job, age)
}
