// Package mirror tees state writes to a secondary store so local
// execution against a remote profile keeps a local SQLite shadow the
// laptop can still browse after the fact.
//
// Scope: STATE ONLY. Per-node log appends, cache writes, and
// concurrency acquires are not mirrored here -- those surfaces stay
// single-write. Logs/cache mirroring may follow if the migration-guide
// caveat changes, but it is a separate decision.
package mirror

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Backend is a storage.StateStore that writes every mutation to a
// canonical store and a best-effort local mirror in parallel. Reads
// delegate to canonical. See New.
type Backend struct {
	canonical storage.StateStore
	local     *store.Store
	logger    *slog.Logger
}

var _ storage.StateStore = (*Backend)(nil)

// New wraps canonical with a mirror that ALSO writes to local. Every
// mutating StateStore method dispatches to both stores in parallel and
// returns when both have completed. canonical's error is returned to the
// caller (canonical is authoritative). local's error is logged at warn
// and otherwise swallowed (local is best-effort). A canonical error does
// NOT abort the local write -- both run to completion so the laptop's
// view stays as complete as possible.
//
// Reads delegate to canonical. The local mirror is write-only here; the
// CLI's read commands hit canonical via the profile's resolved backend
// (OpenReadBackendForProfile).
//
// canonical: the remote/profile state store (e.g. *s3state.Backend or
//
//	*client.Client). MUST not be nil.
//
// local: the local SQLite *store.Store. MUST not be nil. Close on the
//
//	returned wrapper cascades to both stores (see Close).
//
// logger: pass nil to use slog.Default().
func New(canonical storage.StateStore, local *store.Store, logger *slog.Logger) storage.StateStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &Backend{canonical: canonical, local: local, logger: logger}
}

// tee runs canon and local in parallel, waits for both, logs local's
// error at warn (best-effort), and returns canon's error (authoritative).
func (b *Backend) tee(method, runID string, canon, local func() error) error {
	var wg sync.WaitGroup
	var canonErr, localErr error
	wg.Add(2)
	go func() { defer wg.Done(); canonErr = canon() }()
	go func() { defer wg.Done(); localErr = local() }()
	wg.Wait()
	if localErr != nil {
		b.logger.Warn("mirror: local write failed", "method", method, "run_id", runID, "err", localErr)
	}
	return canonErr
}

// Close cascades to both stores: close canonical, close local, return
// the first non-nil error. The orchestrator opens the local store when
// it builds the mirror and has no other Closer handle for it, so the
// run's existing defer opts.State.Close() must reach local through here.
func (b *Backend) Close() error {
	canonErr := b.canonical.Close()
	localErr := b.local.Close()
	if canonErr != nil {
		return canonErr
	}
	return localErr
}

// Runs.

func (b *Backend) CreateRun(ctx context.Context, r store.Run) error {
	return b.tee("CreateRun", r.ID,
		func() error { return b.canonical.CreateRun(ctx, r) },
		func() error { return b.local.CreateRun(ctx, r) })
}

func (b *Backend) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	return b.tee("FinishRun", runID,
		func() error { return b.canonical.FinishRun(ctx, runID, status, errMsg) },
		func() error { return b.local.FinishRun(ctx, runID, status, errMsg) })
}

func (b *Backend) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	return b.tee("UpdatePlanSnapshot", runID,
		func() error { return b.canonical.UpdatePlanSnapshot(ctx, runID, snapshot) },
		func() error { return b.local.UpdatePlanSnapshot(ctx, runID, snapshot) })
}

func (b *Backend) TouchRunHeartbeat(ctx context.Context, runID string) error {
	return b.tee("TouchRunHeartbeat", runID,
		func() error { return b.canonical.TouchRunHeartbeat(ctx, runID) },
		func() error { return b.local.TouchRunHeartbeat(ctx, runID) })
}

func (b *Backend) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	return b.canonical.GetRun(ctx, runID)
}

func (b *Backend) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error) {
	return b.canonical.GetLatestRun(ctx, pipeline, statuses, maxAge)
}

// Nodes.

func (b *Backend) CreateNode(ctx context.Context, n store.Node) error {
	return b.tee("CreateNode", n.RunID,
		func() error { return b.canonical.CreateNode(ctx, n) },
		func() error { return b.local.CreateNode(ctx, n) })
}

func (b *Backend) StartNode(ctx context.Context, runID, nodeID string) error {
	return b.tee("StartNode", runID,
		func() error { return b.canonical.StartNode(ctx, runID, nodeID) },
		func() error { return b.local.StartNode(ctx, runID, nodeID) })
}

func (b *Backend) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return b.tee("FinishNode", runID,
		func() error { return b.canonical.FinishNode(ctx, runID, nodeID, outcome, errMsg, output) },
		func() error { return b.local.FinishNode(ctx, runID, nodeID, outcome, errMsg, output) })
}

func (b *Backend) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	return b.tee("FinishNodeWithReason", runID,
		func() error {
			return b.canonical.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
		},
		func() error {
			return b.local.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
		})
}

func (b *Backend) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	return b.tee("UpdateNodeDeps", runID,
		func() error { return b.canonical.UpdateNodeDeps(ctx, runID, nodeID, deps) },
		func() error { return b.local.UpdateNodeDeps(ctx, runID, nodeID, deps) })
}

func (b *Backend) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	return b.tee("UpdateNodeActivity", runID,
		func() error { return b.canonical.UpdateNodeActivity(ctx, runID, nodeID, detail) },
		func() error { return b.local.UpdateNodeActivity(ctx, runID, nodeID, detail) })
}

func (b *Backend) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	return b.tee("SetNodeStatus", runID,
		func() error { return b.canonical.SetNodeStatus(ctx, runID, nodeID, status) },
		func() error { return b.local.SetNodeStatus(ctx, runID, nodeID, status) })
}

func (b *Backend) GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error) {
	return b.canonical.GetNode(ctx, runID, nodeID)
}

func (b *Backend) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	return b.tee("TouchNodeHeartbeat", runID,
		func() error { return b.canonical.TouchNodeHeartbeat(ctx, runID, nodeID) },
		func() error { return b.local.TouchNodeHeartbeat(ctx, runID, nodeID) })
}

func (b *Backend) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	return b.tee("AppendNodeAnnotation", runID,
		func() error { return b.canonical.AppendNodeAnnotation(ctx, runID, nodeID, msg) },
		func() error { return b.local.AppendNodeAnnotation(ctx, runID, nodeID, msg) })
}

func (b *Backend) SetNodeSummary(ctx context.Context, runID, nodeID, md string) error {
	return b.tee("SetNodeSummary", runID,
		func() error { return b.canonical.SetNodeSummary(ctx, runID, nodeID, md) },
		func() error { return b.local.SetNodeSummary(ctx, runID, nodeID, md) })
}

// Per-step state.

func (b *Backend) StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return b.tee("StartNodeStep", runID,
		func() error { return b.canonical.StartNodeStep(ctx, runID, nodeID, stepID) },
		func() error { return b.local.StartNodeStep(ctx, runID, nodeID, stepID) })
}

func (b *Backend) FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error {
	return b.tee("FinishNodeStep", runID,
		func() error { return b.canonical.FinishNodeStep(ctx, runID, nodeID, stepID, status) },
		func() error { return b.local.FinishNodeStep(ctx, runID, nodeID, stepID, status) })
}

func (b *Backend) SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return b.tee("SkipNodeStep", runID,
		func() error { return b.canonical.SkipNodeStep(ctx, runID, nodeID, stepID) },
		func() error { return b.local.SkipNodeStep(ctx, runID, nodeID, stepID) })
}

func (b *Backend) AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error {
	return b.tee("AppendStepAnnotation", runID,
		func() error { return b.canonical.AppendStepAnnotation(ctx, runID, nodeID, stepID, msg) },
		func() error { return b.local.AppendStepAnnotation(ctx, runID, nodeID, stepID, msg) })
}

func (b *Backend) SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error {
	return b.tee("SetStepSummary", runID,
		func() error { return b.canonical.SetStepSummary(ctx, runID, nodeID, stepID, md) },
		func() error { return b.local.SetStepSummary(ctx, runID, nodeID, stepID, md) })
}

func (b *Backend) ListNodeSteps(ctx context.Context, runID string) ([]*store.NodeStep, error) {
	return b.canonical.ListNodeSteps(ctx, runID)
}

// Metric samples.

func (b *Backend) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error {
	return b.tee("AddNodeMetricSample", runID,
		func() error { return b.canonical.AddNodeMetricSample(ctx, runID, nodeID, sample) },
		func() error { return b.local.AddNodeMetricSample(ctx, runID, nodeID, sample) })
}

// Dispatch snapshots.

func (b *Backend) WriteNodeDispatch(ctx context.Context, d store.NodeDispatch) error {
	return b.tee("WriteNodeDispatch", d.RunID,
		func() error { return b.canonical.WriteNodeDispatch(ctx, d) },
		func() error { return b.local.WriteNodeDispatch(ctx, d) })
}

func (b *Backend) GetNodeDispatch(ctx context.Context, runID, nodeID string, seq int) (*store.NodeDispatch, error) {
	return b.canonical.GetNodeDispatch(ctx, runID, nodeID, seq)
}

func (b *Backend) ListNodeDispatches(ctx context.Context, runID, nodeID string) ([]*store.NodeDispatch, error) {
	return b.canonical.ListNodeDispatches(ctx, runID, nodeID)
}

// Debug pauses.

func (b *Backend) CreateDebugPause(ctx context.Context, p store.DebugPause) error {
	return b.tee("CreateDebugPause", p.RunID,
		func() error { return b.canonical.CreateDebugPause(ctx, p) },
		func() error { return b.local.CreateDebugPause(ctx, p) })
}

func (b *Backend) GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*store.DebugPause, error) {
	return b.canonical.GetActiveDebugPause(ctx, runID, nodeID)
}

func (b *Backend) ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error {
	return b.tee("ReleaseDebugPause", runID,
		func() error { return b.canonical.ReleaseDebugPause(ctx, runID, nodeID, releasedBy, kind) },
		func() error { return b.local.ReleaseDebugPause(ctx, runID, nodeID, releasedBy, kind) })
}

func (b *Backend) ListDebugPauses(ctx context.Context, runID string) ([]*store.DebugPause, error) {
	return b.canonical.ListDebugPauses(ctx, runID)
}

// Triggers.

func (b *Backend) FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error) {
	return b.canonical.FindSpawnedChildTriggerID(ctx, parentRunID, parentNodeID, pipeline)
}

// Approvals.

func (b *Backend) CreateApproval(ctx context.Context, a store.Approval) error {
	return b.tee("CreateApproval", a.RunID,
		func() error { return b.canonical.CreateApproval(ctx, a) },
		func() error { return b.local.CreateApproval(ctx, a) })
}

func (b *Backend) GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error) {
	return b.canonical.GetApproval(ctx, runID, nodeID)
}

// ResolveApproval is a write that also returns the resolved row. Both
// stores are written in parallel; canonical's (value, error) is returned
// and local's error is logged best-effort.
func (b *Backend) ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*store.Approval, error) {
	var wg sync.WaitGroup
	var canonVal *store.Approval
	var canonErr, localErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		canonVal, canonErr = b.canonical.ResolveApproval(ctx, runID, nodeID, resolution, approver, comment)
	}()
	go func() {
		defer wg.Done()
		_, localErr = b.local.ResolveApproval(ctx, runID, nodeID, resolution, approver, comment)
	}()
	wg.Wait()
	if localErr != nil {
		b.logger.Warn("mirror: local write failed", "method", "ResolveApproval", "run_id", runID, "err", localErr)
	}
	return canonVal, canonErr
}

func (b *Backend) ListPendingApprovals(ctx context.Context) ([]*store.Approval, error) {
	return b.canonical.ListPendingApprovals(ctx)
}
