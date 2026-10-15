package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// LocalOutputs reads recorded outputs for a run hosted without a controller claim.
type LocalOutputs interface {
	GetNode(context.Context, string, string) (*store.Node, error)
	GetNodeOutput(context.Context, string, string) ([]byte, error)
	GetLatestRun(context.Context, string, []string, time.Duration) (*store.Run, error)
}

func (g *grant) localOutput(ctx context.Context, job string) (json.RawMessage, error) {
	ancestor, err := localAncestor(ctx, g.Local, g.RunID, g.NodeID, job)
	if errors.Is(err, store.ErrNotFound) {
		return nil, executorError(apierr.UnknownJob, "run has no such job")
	}
	if err != nil {
		return nil, serviceUnavailable()
	}
	if !ancestor {
		return nil, routeFailure(apierr.Forbidden, apierr.ReasonNotDependency, "job output is not a dependency")
	}
	row, err := g.Local.GetNode(ctx, g.RunID, job)
	if err != nil || row == nil {
		return nil, serviceUnavailable()
	}
	if row.Status != "done" {
		return nil, routeFailure(apierr.Conflict, apierr.ReasonNotReady, "job has not finished")
	}
	if row.Outcome != string(sparkwing.Success) && row.Outcome != string(sparkwing.Memoized) {
		return nil, routeFailure(apierr.NotFound, apierr.ReasonNoOutput, "job finished without output")
	}
	return g.readLocalOutput(ctx, g.RunID, job)
}

func (g *grant) localLatest(ctx context.Context, pipeline, job string, age time.Duration) (ProcessOutput, error) {
	if !slices.Contains(g.Refs, nodeproto.PipelineReference{Pipeline: pipeline, Job: job}) {
		return ProcessOutput{}, routeFailure(apierr.Forbidden, apierr.ReasonReferenceUndeclared, "pipeline reference is not declared")
	}
	run, err := g.Local.GetLatestRun(ctx, pipeline, []string{"success"}, age)
	if errors.Is(err, store.ErrNotFound) {
		return ProcessOutput{}, executorError(apierr.NotFound, "no successful pipeline run matches")
	}
	if err != nil || run == nil {
		return ProcessOutput{}, serviceUnavailable()
	}
	raw, err := g.readLocalOutput(ctx, run.ID, job)
	return ProcessOutput{RunID: run.ID, Output: raw}, err
}

func (g *grant) readLocalOutput(ctx context.Context, run, job string) (json.RawMessage, error) {
	raw, err := g.Local.GetNodeOutput(ctx, run, job)
	if errors.Is(err, store.ErrNotFound) {
		return nil, routeFailure(apierr.NotFound, apierr.ReasonNoOutput, "job recorded no output")
	}
	if err != nil {
		return nil, serviceUnavailable()
	}
	if len(raw) == 0 {
		raw = []byte("null")
	}
	return raw, nil
}

func localAncestor(ctx context.Context, st LocalOutputs, runID, node, target string) (bool, error) {
	if _, err := st.GetNode(ctx, runID, target); err != nil {
		return false, err
	}
	seen := map[string]bool{node: true}
	queue := []string{node}
	for len(queue) > 0 {
		row, err := st.GetNode(ctx, runID, queue[0])
		queue = queue[1:]
		// safety: an optional dependency the plan never added has no row and leads nowhere.
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		for _, dep := range row.Deps {
			if dep == target {
				return true, nil
			}
			if !seen[dep] {
				seen[dep] = true
				queue = append(queue, dep)
			}
		}
	}
	return false, nil
}
