package orchestrator

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// mirrorStateBackend tees state writes to a canonical StateBackend and a
// best-effort local SQLite mirror, so `sparkwing run --profile <remote>`
// from a laptop keeps a local shadow of the run the laptop can still
// browse afterward. It operates at the StateBackend layer (not the
// narrower storage.StateStore) because that is what the orchestrator's
// run path consumes: the tee must cover AppendEvent and the rest of the
// StateBackend surface, not just the bare store methods.
//
// Scope: STATE ONLY. Per-node log appends, cache writes, and concurrency
// acquires are not mirrored -- those surfaces stay single-write.
//
// Write semantics: each mutating method dispatches to both backends in
// parallel and returns when both complete. canonical's error is returned
// (canonical is authoritative); local's error is logged at warn and
// otherwise swallowed. A canonical error does not abort the local write.
//
// Read semantics: reads delegate to canonical. The local mirror is
// write-only here; read commands hit canonical via the profile's
// resolved backend (OpenReadBackendForProfile).
//
// EnqueueTrigger is the deliberate exception: it is NOT teed. Spawned
// child triggers are consumed by the canonical's trigger rendezvous
// (controller / Postgres), run elsewhere, and never land in the laptop's
// mirror anyway; teeing would only plant orphan trigger rows with a
// divergent id. It delegates to canonical alone, which also owns cycle
// detection and id authority.
type mirrorStateBackend struct {
	canonical StateBackend
	local     StateBackend
	logger    *slog.Logger
}

var _ StateBackend = (*mirrorStateBackend)(nil)

// newMirrorStateBackend wraps canonical with a local SQLite mirror. The
// local store is adapted to a StateBackend via localState (which already
// supplies AppendEvent / GetNodeOutput / EnqueueTrigger). RunLocal owns
// closing the local store; pass nil logger to use slog.Default().
func newMirrorStateBackend(canonical StateBackend, local *store.Store, logger *slog.Logger) *mirrorStateBackend {
	if logger == nil {
		logger = slog.Default()
	}
	return &mirrorStateBackend{canonical: canonical, local: localState{st: local}, logger: logger}
}

// tee runs canon and local in parallel, waits for both, logs local's
// error at warn (best-effort), and returns canon's error (authoritative).
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

// Close cascades to both backends: close canonical, close local, return
// the first non-nil error. RunLocal normally closes the canonical and
// the local store directly; this exists for interface completeness and
// is safe to call redundantly (sql.DB.Close is idempotent).
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
		func() error { return m.local.CreateRun(ctx, r) })
}

func (m *mirrorStateBackend) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	return m.tee("FinishRun", runID,
		func() error { return m.canonical.FinishRun(ctx, runID, status, errMsg) },
		func() error { return m.local.FinishRun(ctx, runID, status, errMsg) })
}

func (m *mirrorStateBackend) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	return m.tee("UpdatePlanSnapshot", runID,
		func() error { return m.canonical.UpdatePlanSnapshot(ctx, runID, snapshot) },
		func() error { return m.local.UpdatePlanSnapshot(ctx, runID, snapshot) })
}

func (m *mirrorStateBackend) TouchRunHeartbeat(ctx context.Context, runID string) error {
	return m.tee("TouchRunHeartbeat", runID,
		func() error { return m.canonical.TouchRunHeartbeat(ctx, runID) },
		func() error { return m.local.TouchRunHeartbeat(ctx, runID) })
}

func (m *mirrorStateBackend) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	return m.canonical.GetRun(ctx, runID)
}

func (m *mirrorStateBackend) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error) {
	return m.canonical.GetLatestRun(ctx, pipeline, statuses, maxAge)
}

func (m *mirrorStateBackend) CreateNode(ctx context.Context, n store.Node) error {
	return m.tee("CreateNode", n.RunID,
		func() error { return m.canonical.CreateNode(ctx, n) },
		func() error { return m.local.CreateNode(ctx, n) })
}

func (m *mirrorStateBackend) StartNode(ctx context.Context, runID, nodeID string) error {
	return m.tee("StartNode", runID,
		func() error { return m.canonical.StartNode(ctx, runID, nodeID) },
		func() error { return m.local.StartNode(ctx, runID, nodeID) })
}

func (m *mirrorStateBackend) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return m.tee("FinishNode", runID,
		func() error { return m.canonical.FinishNode(ctx, runID, nodeID, outcome, errMsg, output) },
		func() error { return m.local.FinishNode(ctx, runID, nodeID, outcome, errMsg, output) })
}

func (m *mirrorStateBackend) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	return m.tee("FinishNodeWithReason", runID,
		func() error {
			return m.canonical.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
		},
		func() error {
			return m.local.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
		})
}

func (m *mirrorStateBackend) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	return m.tee("UpdateNodeDeps", runID,
		func() error { return m.canonical.UpdateNodeDeps(ctx, runID, nodeID, deps) },
		func() error { return m.local.UpdateNodeDeps(ctx, runID, nodeID, deps) })
}

func (m *mirrorStateBackend) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	return m.tee("UpdateNodeActivity", runID,
		func() error { return m.canonical.UpdateNodeActivity(ctx, runID, nodeID, detail) },
		func() error { return m.local.UpdateNodeActivity(ctx, runID, nodeID, detail) })
}

func (m *mirrorStateBackend) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	return m.tee("SetNodeStatus", runID,
		func() error { return m.canonical.SetNodeStatus(ctx, runID, nodeID, status) },
		func() error { return m.local.SetNodeStatus(ctx, runID, nodeID, status) })
}

func (m *mirrorStateBackend) SetNodeArtifactManifest(ctx context.Context, runID, nodeID, manifestDigest string) error {
	return m.tee("SetNodeArtifactManifest", runID,
		func() error { return m.canonical.SetNodeArtifactManifest(ctx, runID, nodeID, manifestDigest) },
		func() error { return m.local.SetNodeArtifactManifest(ctx, runID, nodeID, manifestDigest) })
}

func (m *mirrorStateBackend) GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error) {
	return m.canonical.GetNode(ctx, runID, nodeID)
}

func (m *mirrorStateBackend) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	return m.tee("TouchNodeHeartbeat", runID,
		func() error { return m.canonical.TouchNodeHeartbeat(ctx, runID, nodeID) },
		func() error { return m.local.TouchNodeHeartbeat(ctx, runID, nodeID) })
}

func (m *mirrorStateBackend) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	return m.tee("AppendNodeAnnotation", runID,
		func() error { return m.canonical.AppendNodeAnnotation(ctx, runID, nodeID, msg) },
		func() error { return m.local.AppendNodeAnnotation(ctx, runID, nodeID, msg) })
}

func (m *mirrorStateBackend) SetNodeSummary(ctx context.Context, runID, nodeID, md string) error {
	return m.tee("SetNodeSummary", runID,
		func() error { return m.canonical.SetNodeSummary(ctx, runID, nodeID, md) },
		func() error { return m.local.SetNodeSummary(ctx, runID, nodeID, md) })
}

func (m *mirrorStateBackend) StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return m.tee("StartNodeStep", runID,
		func() error { return m.canonical.StartNodeStep(ctx, runID, nodeID, stepID) },
		func() error { return m.local.StartNodeStep(ctx, runID, nodeID, stepID) })
}

func (m *mirrorStateBackend) FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error {
	return m.tee("FinishNodeStep", runID,
		func() error { return m.canonical.FinishNodeStep(ctx, runID, nodeID, stepID, status) },
		func() error { return m.local.FinishNodeStep(ctx, runID, nodeID, stepID, status) })
}

func (m *mirrorStateBackend) SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return m.tee("SkipNodeStep", runID,
		func() error { return m.canonical.SkipNodeStep(ctx, runID, nodeID, stepID) },
		func() error { return m.local.SkipNodeStep(ctx, runID, nodeID, stepID) })
}

func (m *mirrorStateBackend) AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error {
	return m.tee("AppendStepAnnotation", runID,
		func() error { return m.canonical.AppendStepAnnotation(ctx, runID, nodeID, stepID, msg) },
		func() error { return m.local.AppendStepAnnotation(ctx, runID, nodeID, stepID, msg) })
}

func (m *mirrorStateBackend) SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error {
	return m.tee("SetStepSummary", runID,
		func() error { return m.canonical.SetStepSummary(ctx, runID, nodeID, stepID, md) },
		func() error { return m.local.SetStepSummary(ctx, runID, nodeID, stepID, md) })
}

func (m *mirrorStateBackend) ListNodeSteps(ctx context.Context, runID string) ([]*store.NodeStep, error) {
	return m.canonical.ListNodeSteps(ctx, runID)
}

func (m *mirrorStateBackend) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error {
	return m.tee("AddNodeMetricSample", runID,
		func() error { return m.canonical.AddNodeMetricSample(ctx, runID, nodeID, sample) },
		func() error { return m.local.AddNodeMetricSample(ctx, runID, nodeID, sample) })
}

func (m *mirrorStateBackend) WriteNodeDispatch(ctx context.Context, d store.NodeDispatch) error {
	return m.tee("WriteNodeDispatch", d.RunID,
		func() error { return m.canonical.WriteNodeDispatch(ctx, d) },
		func() error { return m.local.WriteNodeDispatch(ctx, d) })
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
		func() error { return m.local.CreateDebugPause(ctx, p) })
}

func (m *mirrorStateBackend) GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*store.DebugPause, error) {
	return m.canonical.GetActiveDebugPause(ctx, runID, nodeID)
}

func (m *mirrorStateBackend) ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error {
	return m.tee("ReleaseDebugPause", runID,
		func() error { return m.canonical.ReleaseDebugPause(ctx, runID, nodeID, releasedBy, kind) },
		func() error { return m.local.ReleaseDebugPause(ctx, runID, nodeID, releasedBy, kind) })
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
		func() error { return m.local.CreateApproval(ctx, a) })
}

func (m *mirrorStateBackend) GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error) {
	return m.canonical.GetApproval(ctx, runID, nodeID)
}

// ResolveApproval is a write that returns the resolved row. Both
// backends are written in parallel; canonical's (value, error) is
// returned and local's error logged best-effort.
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
		_, localErr = m.local.ResolveApproval(ctx, runID, nodeID, resolution, approver, comment)
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

// AppendEvent is a write and is teed: run-level events (run_start, node
// events, ...) are part of the state the laptop mirror should reflect.
func (m *mirrorStateBackend) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	return m.tee("AppendEvent", runID,
		func() error { return m.canonical.AppendEvent(ctx, runID, nodeID, kind, payload) },
		func() error { return m.local.AppendEvent(ctx, runID, nodeID, kind, payload) })
}

// GetNodeOutput is a read; delegate to canonical.
func (m *mirrorStateBackend) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	return m.canonical.GetNodeOutput(ctx, runID, nodeID)
}

// EnqueueTrigger is NOT teed -- see the type doc. Canonical owns trigger
// rendezvous, cycle detection, and id authority.
func (m *mirrorStateBackend) EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string) (string, error) {
	return m.canonical.EnqueueTrigger(ctx, pipeline, args, parentRunID, parentNodeID, retryOf, source, user, repo, branch)
}

// EnqueueTriggerWithEnv is NOT teed for the same reason as EnqueueTrigger:
// the canonical backend owns child-trigger identity and rendezvous. The
// trigger env is still part of that canonical enqueue contract.
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
