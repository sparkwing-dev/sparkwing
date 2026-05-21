package storage

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StateStore is the run-record store: runs, nodes, steps, annotations,
// approvals, dispatches, debug pauses, and the schema migrations the
// orchestrator depends on.
//
// State is opened from a backends.Spec via
// pkg/storage/storeurl.OpenStateStoreFromSpec. The SQLite-backed
// *store.Store is the only implementation today; Postgres, HTTP
// (controller), and object-store NDJSON backends plug in behind the
// same interface as later units land. Methods whose wrappers add
// adapter logic on top of the store (output extraction, trigger
// cycle detection, simplified-error AppendEvent) live on the
// orchestrator's runtime interface that embeds this one, not here.
type StateStore interface {
	// Lifecycle.
	Close() error

	// Runs.
	CreateRun(ctx context.Context, r store.Run) error
	FinishRun(ctx context.Context, runID, status, errMsg string) error
	UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error
	GetRun(ctx context.Context, runID string) (*store.Run, error)
	GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error)

	// Nodes.
	CreateNode(ctx context.Context, n store.Node) error
	StartNode(ctx context.Context, runID, nodeID string) error
	FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error
	FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error
	UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error
	UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error
	SetNodeStatus(ctx context.Context, runID, nodeID, status string) error
	GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error)
	TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error
	AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error
	SetNodeSummary(ctx context.Context, runID, nodeID, md string) error

	// Per-step state.
	StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error
	FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error
	SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error
	AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error
	SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error
	ListNodeSteps(ctx context.Context, runID string) ([]*store.NodeStep, error)

	// Metric samples (advisory; drop-on-error is acceptable).
	AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error

	// Dispatch-snapshot surface. seq < 0 fetches the latest attempt.
	WriteNodeDispatch(ctx context.Context, d store.NodeDispatch) error
	GetNodeDispatch(ctx context.Context, runID, nodeID string, seq int) (*store.NodeDispatch, error)
	ListNodeDispatches(ctx context.Context, runID, nodeID string) ([]*store.NodeDispatch, error)

	// Debug-pause surface.
	CreateDebugPause(ctx context.Context, p store.DebugPause) error
	GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*store.DebugPause, error)
	ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error
	ListDebugPauses(ctx context.Context, runID string) ([]*store.DebugPause, error)

	// Trigger lookup. EnqueueTrigger is intentionally not here -- it
	// carries cycle-detection logic that lives in the orchestrator's
	// adapter, not in the storage handle.
	FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error)

	// Approvals.
	CreateApproval(ctx context.Context, a store.Approval) error
	GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error)
	ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*store.Approval, error)
	ListPendingApprovals(ctx context.Context) ([]*store.Approval, error)
}

// Compile-time check: the SQLite-backed *store.Store satisfies
// StateStore. Future backends (Postgres, controller HTTP, object-store
// NDJSON) wire similar assertions next to their own constructors.
var _ StateStore = (*store.Store)(nil)
