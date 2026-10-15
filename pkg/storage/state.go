package storage

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StateStoreOld supplies the legacy orchestrator state operations.
type StateStoreOld interface {
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
	GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error)
	AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error
	SetNodeSummary(ctx context.Context, runID, nodeID, md string) error

	// Per-step state.
	StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error
	FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error
	SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error
	AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error
	SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error

	// Events. Payload is stored as given; most kinds carry JSON.
	AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error

	// Metric samples (advisory; drop-on-error is acceptable).
	AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error

	// Debug-pause surface.
	CreateDebugPause(ctx context.Context, p store.DebugPause) error
	GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*store.DebugPause, error)
	ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error

	FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error)

	// Approvals.
	CreateApproval(ctx context.Context, a store.Approval) error
	GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error)
	ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*store.Approval, error)
}

var _ StateStoreOld = (*store.Store)(nil)
