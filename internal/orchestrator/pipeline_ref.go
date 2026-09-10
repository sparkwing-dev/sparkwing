package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// pipelineRefState is the state surface a cross-pipeline ref reads.
type pipelineRefState interface {
	GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error)
	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)
	AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error
}

// newPipelineRefResolver reads the newest successful run of another
// pipeline for Ref.Get and Ref.TryGet. It records an audit event against
// consumerRunID naming the run it read, so the consuming node's event
// stream shows what fed it; warnAudit reports an event that could not be
// recorded, which leaves the resolution itself intact.
func newPipelineRefResolver(
	state pipelineRefState,
	consumerRunID string,
	warnAudit func(ctx context.Context, node string, err error),
) sparkwing.PipelineResolver {
	return sparkwing.PipelineResolverFunc(func(ctx context.Context, pipeline, nodeID string, maxAge time.Duration) (*sparkwing.ResolvedPipelineRef, error) {
		run, err := state.GetLatestRun(ctx, pipeline, []string{"success"}, maxAge)
		if err != nil {
			return nil, fmt.Errorf("no matching run for pipeline %q (maxAge=%s): %w", pipeline, maxAge, absentIfNotFound(err))
		}
		output, err := state.GetNodeOutput(ctx, run.ID, nodeID)
		if err != nil {
			return nil, fmt.Errorf("get node %s/%s output: %w", run.ID, nodeID, absentIfNotFound(err))
		}
		if currentNode := sparkwing.NodeFromContext(ctx); currentNode != "" {
			payload, mErr := json.Marshal(map[string]any{
				"pipeline":        pipeline,
				"node_id":         nodeID,
				"source_run_id":   run.ID,
				"max_age_seconds": int64(maxAge.Seconds()),
				"source_finished": run.FinishedAt,
			})
			if mErr != nil {
				warnAudit(ctx, currentNode, mErr)
			} else if evErr := state.AppendEvent(ctx, consumerRunID, currentNode,
				"pipeline_ref_resolved", payload); evErr != nil {
				warnAudit(ctx, currentNode, evErr)
			}
		}
		return &sparkwing.ResolvedPipelineRef{RunID: run.ID, Data: output}, nil
	})
}

// absentIfNotFound marks a store miss as the absence Ref.TryGet reports
// rather than panics on. Every other failure stays unmarked, so a store
// the resolver could not reach crashes the step instead of reading as a
// pipeline's first run.
func absentIfNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %w", err, sparkwing.ErrRefAbsent)
	}
	return err
}
