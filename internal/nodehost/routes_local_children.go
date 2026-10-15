package nodehost

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ErrChildCycle is a child run that would start a pipeline already among the
// parent run's ancestors.
var ErrChildCycle = errors.New("the pipeline is already among this run's ancestors")

// ChildParent is the run and node a child run is started from. The child's
// admission attaches to what that node holds.
type ChildParent struct {
	RunID, NodeID string
}

// LocalChildren starts and reads the child runs of a run that no controller
// holds, through this machine's trigger queue.
type LocalChildren interface {
	StartChild(ctx context.Context, parent ChildParent, req ChildRequest) (runID string, err error)
	GetRun(ctx context.Context, runID string) (*store.Run, error)
	// Queued reports whether the child run waits in plan admission.
	Queued(ctx context.Context, runID string) (bool, error)
}

// perf: a local store read is cheap, so a child's end reaches its parent within a quarter second.
const localAwaitPoll = 250 * time.Millisecond

func (g *grant) localStartChild(ctx context.Context, request ChildRequest) (string, error) {
	id, err := g.Children.StartChild(ctx, ChildParent{RunID: g.RunID, NodeID: g.NodeID}, request)
	if errors.Is(err, ErrChildCycle) {
		return "", routeFailure(apierr.Conflict, apierr.ReasonChildCycle, "pipeline is already an ancestor")
	}
	if err != nil {
		return "", serviceUnavailable()
	}
	g.started.Store(id, true)
	return id, nil
}

func (g *grant) localAwaitChild(ctx context.Context, child, job string, wait time.Duration) (ProcessChildResult, error) {
	if _, ok := g.started.Load(child); !ok {
		return ProcessChildResult{}, executorError(apierr.NotFound, "process did not start this child")
	}
	if g.Pause != nil {
		defer g.Pause()()
	}
	var resumeWall func()
	defer func() {
		if resumeWall != nil {
			resumeWall()
		}
	}()
	deadline := time.Now().Add(wait)
	result := ProcessChildResult{RunID: child, Status: "pending"}
	for {
		run, err := g.Children.GetRun(ctx, child)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return ProcessChildResult{}, serviceUnavailable()
		default:
			result.Status = run.Status
			if run.Error != "" {
				result.Error = &apierr.Error{Code: apierr.SDKError, Message: run.Error}
			}
		}
		if g.Hold != nil && !terminalRun(result.Status) {
			queued, err := g.Children.Queued(ctx, child)
			switch {
			case err == nil && queued && resumeWall == nil:
				resumeWall = g.Hold()
			case err == nil && !queued && resumeWall != nil:
				resumeWall()
				resumeWall = nil
			}
		}
		if terminalRun(result.Status) || !time.Now().Before(deadline) {
			break
		}
		timer := time.NewTimer(min(localAwaitPoll, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ProcessChildResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	if result.Status != "success" || job == "" {
		return result, nil
	}
	if g.Local == nil {
		return ProcessChildResult{}, serviceUnavailable()
	}
	if _, err := g.Local.GetNode(ctx, child, job); errors.Is(err, store.ErrNotFound) {
		return ProcessChildResult{}, executorError(apierr.UnknownJob, "child has no such job")
	} else if err != nil {
		return ProcessChildResult{}, serviceUnavailable()
	}
	var err error
	result.Output, err = g.readLocalOutput(ctx, child, job)
	return result, err
}
