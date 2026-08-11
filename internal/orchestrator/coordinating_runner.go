package orchestrator

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type nodeExecutor func(context.Context, runner.Request) runner.Result

type coordinatingRunner struct {
	coordinator *InProcessRunner
	downstream  runner.Runner
}

func newCoordinatingRunner(backends Backends, downstream runner.Runner) runner.Runner {
	if _, ok := downstream.(runner.DownstreamCoordinator); ok {
		return downstream
	}
	base := &coordinatingRunner{
		coordinator: NewInProcessRunner(backends),
		downstream:  downstream,
	}
	if labels, ok := downstream.(runner.LabelAdvertiser); ok {
		return &labeledCoordinatingRunner{coordinatingRunner: base, labels: labels}
	}
	return base
}

func (r *coordinatingRunner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	return r.coordinator.runCoordinated(withForceReleaseDisabled(ctx), req, r.downstream.RunNode)
}

type labeledCoordinatingRunner struct {
	*coordinatingRunner
	labels runner.LabelAdvertiser
}

func (r *labeledCoordinatingRunner) AdvertisedLabels() []string {
	return r.labels.AdvertisedLabels()
}

func (r *InProcessRunner) runCoordinated(ctx context.Context, req runner.Request, execute nodeExecutor) runner.Result {
	if req.Node == nil {
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("runner coordination: Request.Node is nil for %s/%s", req.RunID, req.NodeID),
		}
	}
	if result, handled := r.runNodeWithCache(ctx, req, execute); handled {
		return result
	}
	if reason, skip := evalSkipPredicates(ctx, req.Node); skip {
		r.markSkipped(ctx, req.RunID, req.Node.ID(), reason)
		return runner.Result{Outcome: sparkwing.Skipped}
	}
	return r.executeWithLocalAdmission(ctx, req, execute)
}

func (r *InProcessRunner) executeWithLocalAdmission(ctx context.Context, req runner.Request, execute nodeExecutor) runner.Result {
	la, _, hostAdmitted := localAdmissionFromContext(ctx)
	if la == nil || hostAdmitted {
		return execute(ctx, req)
	}
	nodeID := req.NodeID
	if nodeID == "" {
		nodeID = req.Node.ID()
	}
	if req.ReleaseWorkerSlot != nil {
		req.ReleaseWorkerSlot()
	}
	priority := localAdmissionPriorityFromContext(ctx)
	lease, err := la.admitNode(ctx, r.backends, req.Pipeline, req.RunID, nodeID, req.Node, priority)
	if req.ReacquireWorkerSlot != nil && !req.ReacquireWorkerSlot() {
		if lease != nil {
			lease.release()
		}
		r.markFailed(ctx, req.RunID, nodeID, ctx.Err())
		return runner.Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
	}
	if err != nil {
		r.markFailed(ctx, req.RunID, nodeID, err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	defer lease.release()
	childToken := localAdmissionChildTokenFromContext(ctx)
	if childToken == "" {
		childToken = lease.token
	}
	nodeCtx := withLocalAdmission(ctx, la, lease.token, childToken, lease.hostAdmitted, priority)
	return execute(nodeCtx, req)
}
