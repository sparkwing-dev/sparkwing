package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type mirrorStateBackend struct {
	canonical StateBackend
	local     StateBackend
	logger    *slog.Logger
}

var _ StateBackend = (*mirrorStateBackend)(nil)

func newMirrorStateBackend(canonical StateBackend, local *store.Store, logger *slog.Logger) *mirrorStateBackend {
	if logger == nil {
		logger = slog.Default()
	}
	return &mirrorStateBackend{canonical: canonical, local: localState{st: local}, logger: logger}
}

func (m *mirrorStateBackend) tee(method, runID string, canon, local func() error) error {
	var wg sync.WaitGroup
	var canonErr, localErr error
	wg.Add(2)
	go func() { defer wg.Done(); canonErr = canon() }()
	go func() { defer wg.Done(); localErr = local() }()
	wg.Wait()
	if localErr != nil {
		m.logger.Warn("mirror: local state write failed", "method", method, "run_id", runID, "err", localErr)
	}
	return canonErr
}

func (m *mirrorStateBackend) Close() error {
	canonErr := m.canonical.Close()
	localErr := m.local.Close()
	if canonErr != nil {
		return canonErr
	}
	return localErr
}

func (m *mirrorStateBackend) CreateRun(ctx context.Context, r store.Run) error {
	return m.tee("CreateRun", r.ID,
		func() error { return m.canonical.CreateRun(ctx, r) },
		func() error { return m.local.CreateRun(store.WithoutClaimFences(ctx), r) })
}

func (m *mirrorStateBackend) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	return m.tee("FinishRun", runID,
		func() error { return m.canonical.FinishRun(ctx, runID, status, errMsg) },
		func() error { return m.local.FinishRun(store.WithoutClaimFences(ctx), runID, status, errMsg) })
}

func (m *mirrorStateBackend) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	return m.tee("UpdatePlanSnapshot", runID,
		func() error { return m.canonical.UpdatePlanSnapshot(ctx, runID, snapshot) },
		func() error { return m.local.UpdatePlanSnapshot(store.WithoutClaimFences(ctx), runID, snapshot) })
}

func (m *mirrorStateBackend) TouchRunHeartbeat(ctx context.Context, runID string) error {
	return m.tee("TouchRunHeartbeat", runID,
		func() error { return m.canonical.TouchRunHeartbeat(ctx, runID) },
		func() error { return m.local.TouchRunHeartbeat(store.WithoutClaimFences(ctx), runID) })
}

func (m *mirrorStateBackend) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	return m.canonical.GetRun(ctx, runID)
}

func (m *mirrorStateBackend) GetRunForExecution(ctx context.Context, runID string) (*store.Run, error) {
	return runForExecution(ctx, m.canonical, runID)
}

func (m *mirrorStateBackend) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error) {
	return m.canonical.GetLatestRun(ctx, pipeline, statuses, maxAge)
}

func (m *mirrorStateBackend) CreateNode(ctx context.Context, n store.Node) error {
	return m.tee("CreateNode", n.RunID,
		func() error { return m.canonical.CreateNode(ctx, n) },
		func() error { return m.local.CreateNode(store.WithoutClaimFences(ctx), n) })
}

func (m *mirrorStateBackend) StartNode(ctx context.Context, runID, nodeID string) error {
	return m.tee("StartNode", runID,
		func() error { return m.canonical.StartNode(ctx, runID, nodeID) },
		func() error { return m.local.StartNode(store.WithoutClaimFences(ctx), runID, nodeID) })
}

func (m *mirrorStateBackend) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return m.tee("FinishNode", runID,
		func() error { return m.canonical.FinishNode(ctx, runID, nodeID, outcome, errMsg, output) },
		func() error {
			return m.local.FinishNode(store.WithoutClaimFences(ctx), runID, nodeID, outcome, errMsg, output)
		})
}

func (m *mirrorStateBackend) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	return m.tee("FinishNodeWithReason", runID,
		func() error {
			return m.canonical.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
		},
		func() error {
			return m.local.FinishNodeWithReason(store.WithoutClaimFences(ctx), runID, nodeID, outcome, errMsg, output, reason, exitCode)
		})
}

func (m *mirrorStateBackend) ResetNodeForAutoRetry(ctx context.Context, runID, nodeID string) error {
	return m.tee("ResetNodeForAutoRetry", runID,
		func() error { return resetNodeForAutoRetry(ctx, m.canonical, runID, nodeID) },
		func() error { return resetNodeForAutoRetry(store.WithoutClaimFences(ctx), m.local, runID, nodeID) })
}

func (m *mirrorStateBackend) AcknowledgeNodeExecutionStart(ctx context.Context, runID, nodeID string, start store.ExecutionStart) error {
	recorder, ok := m.canonical.(executionStartAcknowledger)
	if !ok {
		return errors.New("canonical state backend does not support execution-attempt acknowledgement")
	}
	return recorder.AcknowledgeNodeExecutionStart(ctx, runID, nodeID, start)
}

func (m *mirrorStateBackend) FinishNodeExecutionAttempt(ctx context.Context, runID, nodeID string, finish store.ExecutionAttemptFinish) error {
	recorder, ok := m.canonical.(executionStartAcknowledger)
	if !ok {
		return errors.New("canonical state backend does not support execution-attempt acknowledgement")
	}
	return recorder.FinishNodeExecutionAttempt(ctx, runID, nodeID, finish)
}

func (m *mirrorStateBackend) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	return m.tee("UpdateNodeDeps", runID,
		func() error { return m.canonical.UpdateNodeDeps(ctx, runID, nodeID, deps) },
		func() error { return m.local.UpdateNodeDeps(store.WithoutClaimFences(ctx), runID, nodeID, deps) })
}

func (m *mirrorStateBackend) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	return m.tee("UpdateNodeActivity", runID,
		func() error { return m.canonical.UpdateNodeActivity(ctx, runID, nodeID, detail) },
		func() error { return m.local.UpdateNodeActivity(store.WithoutClaimFences(ctx), runID, nodeID, detail) })
}

func (m *mirrorStateBackend) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	return m.tee("SetNodeStatus", runID,
		func() error { return m.canonical.SetNodeStatus(ctx, runID, nodeID, status) },
		func() error { return m.local.SetNodeStatus(store.WithoutClaimFences(ctx), runID, nodeID, status) })
}

func (m *mirrorStateBackend) SetNodeArtifactManifest(ctx context.Context, runID, nodeID, manifestDigest string) error {
	return m.tee("SetNodeArtifactManifest", runID,
		func() error { return m.canonical.SetNodeArtifactManifest(ctx, runID, nodeID, manifestDigest) },
		func() error {
			return m.local.SetNodeArtifactManifest(store.WithoutClaimFences(ctx), runID, nodeID, manifestDigest)
		})
}

func (m *mirrorStateBackend) GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error) {
	return m.canonical.GetNode(ctx, runID, nodeID)
}

func (m *mirrorStateBackend) ListNodes(ctx context.Context, runID string) ([]*store.Node, error) {
	return listRetryNodes(ctx, m.canonical, runID)
}

func (m *mirrorStateBackend) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	return m.tee("TouchNodeHeartbeat", runID,
		func() error { return m.canonical.TouchNodeHeartbeat(ctx, runID, nodeID) },
		func() error { return m.local.TouchNodeHeartbeat(store.WithoutClaimFences(ctx), runID, nodeID) })
}

func (m *mirrorStateBackend) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	return m.tee("AppendNodeAnnotation", runID,
		func() error { return m.canonical.AppendNodeAnnotation(ctx, runID, nodeID, msg) },
		func() error { return m.local.AppendNodeAnnotation(store.WithoutClaimFences(ctx), runID, nodeID, msg) })
}

func (m *mirrorStateBackend) SetNodeSummary(ctx context.Context, runID, nodeID, md string) error {
	return m.tee("SetNodeSummary", runID,
		func() error { return m.canonical.SetNodeSummary(ctx, runID, nodeID, md) },
		func() error { return m.local.SetNodeSummary(store.WithoutClaimFences(ctx), runID, nodeID, md) })
}

func (m *mirrorStateBackend) StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return m.tee("StartNodeStep", runID,
		func() error { return m.canonical.StartNodeStep(ctx, runID, nodeID, stepID) },
		func() error { return m.local.StartNodeStep(store.WithoutClaimFences(ctx), runID, nodeID, stepID) })
}

func (m *mirrorStateBackend) FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error {
	return m.tee("FinishNodeStep", runID,
		func() error { return m.canonical.FinishNodeStep(ctx, runID, nodeID, stepID, status) },
		func() error {
			return m.local.FinishNodeStep(store.WithoutClaimFences(ctx), runID, nodeID, stepID, status)
		})
}

func (m *mirrorStateBackend) SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return m.tee("SkipNodeStep", runID,
		func() error { return m.canonical.SkipNodeStep(ctx, runID, nodeID, stepID) },
		func() error { return m.local.SkipNodeStep(store.WithoutClaimFences(ctx), runID, nodeID, stepID) })
}

func (m *mirrorStateBackend) AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error {
	return m.tee("AppendStepAnnotation", runID,
		func() error { return m.canonical.AppendStepAnnotation(ctx, runID, nodeID, stepID, msg) },
		func() error {
			return m.local.AppendStepAnnotation(store.WithoutClaimFences(ctx), runID, nodeID, stepID, msg)
		})
}

func (m *mirrorStateBackend) SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error {
	return m.tee("SetStepSummary", runID,
		func() error { return m.canonical.SetStepSummary(ctx, runID, nodeID, stepID, md) },
		func() error { return m.local.SetStepSummary(store.WithoutClaimFences(ctx), runID, nodeID, stepID, md) })
}

func (m *mirrorStateBackend) ListNodeSteps(ctx context.Context, runID string) ([]*store.NodeStep, error) {
	return m.canonical.ListNodeSteps(ctx, runID)
}

func (m *mirrorStateBackend) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error {
	return m.tee("AddNodeMetricSample", runID,
		func() error { return m.canonical.AddNodeMetricSample(ctx, runID, nodeID, sample) },
		func() error { return m.local.AddNodeMetricSample(store.WithoutClaimFences(ctx), runID, nodeID, sample) })
}

func (m *mirrorStateBackend) WriteNodeDispatch(ctx context.Context, d store.NodeDispatch) error {
	return m.tee("WriteNodeDispatch", d.RunID,
		func() error { return m.canonical.WriteNodeDispatch(ctx, d) },
		func() error { return m.local.WriteNodeDispatch(store.WithoutClaimFences(ctx), d) })
}

func (m *mirrorStateBackend) GetNodeDispatch(ctx context.Context, runID, nodeID string, seq int) (*store.NodeDispatch, error) {
	return m.canonical.GetNodeDispatch(ctx, runID, nodeID, seq)
}

func (m *mirrorStateBackend) ListNodeDispatches(ctx context.Context, runID, nodeID string) ([]*store.NodeDispatch, error) {
	return m.canonical.ListNodeDispatches(ctx, runID, nodeID)
}

func (m *mirrorStateBackend) CreateDebugPause(ctx context.Context, p store.DebugPause) error {
	return m.tee("CreateDebugPause", p.RunID,
		func() error { return m.canonical.CreateDebugPause(ctx, p) },
		func() error { return m.local.CreateDebugPause(store.WithoutClaimFences(ctx), p) })
}

func (m *mirrorStateBackend) GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*store.DebugPause, error) {
	return m.canonical.GetActiveDebugPause(ctx, runID, nodeID)
}

func (m *mirrorStateBackend) ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error {
	return m.tee("ReleaseDebugPause", runID,
		func() error { return m.canonical.ReleaseDebugPause(ctx, runID, nodeID, releasedBy, kind) },
		func() error {
			return m.local.ReleaseDebugPause(store.WithoutClaimFences(ctx), runID, nodeID, releasedBy, kind)
		})
}

func (m *mirrorStateBackend) ListDebugPauses(ctx context.Context, runID string) ([]*store.DebugPause, error) {
	return m.canonical.ListDebugPauses(ctx, runID)
}

func (m *mirrorStateBackend) FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error) {
	return m.canonical.FindSpawnedChildTriggerID(ctx, parentRunID, parentNodeID, pipeline)
}

func (m *mirrorStateBackend) CreateApproval(ctx context.Context, a store.Approval) error {
	return m.tee("CreateApproval", a.RunID,
		func() error { return m.canonical.CreateApproval(ctx, a) },
		func() error { return m.local.CreateApproval(store.WithoutClaimFences(ctx), a) })
}

func (m *mirrorStateBackend) GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error) {
	return m.canonical.GetApproval(ctx, runID, nodeID)
}

func (m *mirrorStateBackend) ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*store.Approval, error) {
	var wg sync.WaitGroup
	var canonVal *store.Approval
	var canonErr, localErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		canonVal, canonErr = m.canonical.ResolveApproval(ctx, runID, nodeID, resolution, approver, comment)
	}()
	go func() {
		defer wg.Done()
		_, localErr = m.local.ResolveApproval(store.WithoutClaimFences(ctx), runID, nodeID, resolution, approver, comment)
	}()
	wg.Wait()
	if localErr != nil {
		m.logger.Warn("mirror: local state write failed", "method", "ResolveApproval", "run_id", runID, "err", localErr)
	}
	return canonVal, canonErr
}

func (m *mirrorStateBackend) ListPendingApprovals(ctx context.Context) ([]*store.Approval, error) {
	return m.canonical.ListPendingApprovals(ctx)
}

func (m *mirrorStateBackend) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	return m.tee("AppendEvent", runID,
		func() error { return m.canonical.AppendEvent(ctx, runID, nodeID, kind, payload) },
		func() error { return m.local.AppendEvent(store.WithoutClaimFences(ctx), runID, nodeID, kind, payload) })
}

func (m *mirrorStateBackend) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	return m.canonical.GetNodeOutput(ctx, runID, nodeID)
}

func (m *mirrorStateBackend) EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string) (string, error) {
	return m.canonical.EnqueueTrigger(ctx, pipeline, args, parentRunID, parentNodeID, retryOf, source, user, repo, branch)
}

func (m *mirrorStateBackend) EnqueueTriggerWithEnv(
	ctx context.Context,
	pipeline string,
	args map[string]string,
	parentRunID string,
	parentNodeID string,
	retryOf string,
	source string,
	user string,
	repo string,
	branch string,
	triggerEnv map[string]string,
) (string, error) {
	return enqueueTriggerWithEnv(
		ctx, m.canonical, pipeline, args, parentRunID, parentNodeID,
		retryOf, source, user, repo, branch, triggerEnv,
	)
}
