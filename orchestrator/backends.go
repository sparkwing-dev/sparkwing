package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// Backends bundles the infrastructure interfaces the orchestrator
// depends on.
type Backends struct {
	State       StateBackend
	Logs        LogBackend
	Concurrency ConcurrencyBackend
}

// StateBackend persists run/node/event/cache state.
type StateBackend interface {
	CreateRun(ctx context.Context, r store.Run) error
	FinishRun(ctx context.Context, runID, status, errMsg string) error
	UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error

	CreateNode(ctx context.Context, n store.Node) error
	StartNode(ctx context.Context, runID, nodeID string) error
	FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error
	// FinishNodeWithReason adds a structured failure reason
	// (store.Failure*) + optional exit code.
	FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error
	UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error

	// UpdateNodeActivity stamps status_detail and bumps last_heartbeat.
	UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error

	// TouchNodeHeartbeat bumps last_heartbeat without status changes.
	TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error

	AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error

	// AddNodeMetricSample is advisory; drop-on-error is acceptable.
	AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error

	// GetLatestRun returns the latest pipeline run matching statuses
	// within maxAge. ErrNotFound when nothing matches.
	GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error)

	// GetNodeOutput returns a finished node's raw JSON output.
	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)

	// GetNode reads a node row in full (status + outcome + output).
	GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error)

	// GetRun reads one run row.
	GetRun(ctx context.Context, runID string) (*store.Run, error)

	// Dispatch-snapshot surface. seq < 0 fetches the latest attempt.
	WriteNodeDispatch(ctx context.Context, d store.NodeDispatch) error
	GetNodeDispatch(ctx context.Context, runID, nodeID string, seq int) (*store.NodeDispatch, error)
	ListNodeDispatches(ctx context.Context, runID, nodeID string) ([]*store.NodeDispatch, error)

	// Debug-pause surface.
	CreateDebugPause(ctx context.Context, p store.DebugPause) error
	GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*store.DebugPause, error)
	ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error
	ListDebugPauses(ctx context.Context, runID string) ([]*store.DebugPause, error)
	SetNodeStatus(ctx context.Context, runID, nodeID, status string) error

	// EnqueueTrigger spawns a new trigger; cycles are rejected with
	// a wrapped error mentioning "cycle". parentNodeID + retryOf
	// thread retry lineage across nested spawns.
	EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string) (runID string, err error)

	// FindSpawnedChildTriggerID returns the most-recent child trigger
	// at (parentRunID, parentNodeID) for pipeline, or "" + nil.
	FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error)

	// Approval-gate surface.
	CreateApproval(ctx context.Context, a store.Approval) error
	GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error)
	ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*store.Approval, error)
	ListPendingApprovals(ctx context.Context) ([]*store.Approval, error)
}

// LogBackend issues per-node log sinks.
type LogBackend interface {
	OpenNodeLog(runID, nodeID string, delegate sparkwing.Logger) (NodeLog, error)
}

// NodeLog is a sparkwing.Logger with Close. No writes after Close.
type NodeLog interface {
	sparkwing.Logger
	Close() error
}

// ConcurrencyBackend mediates the unified .Cache() DSL: atomic
// acquire (granted/skipped/failed/cached/queued/coalesced), waiter
// resolution, memoizing release (which also promotes waiters), and
// heartbeats that surface the supersede signal.
type ConcurrencyBackend interface {
	AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error)
	HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (expires time.Time, superseded bool, err error)
	ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error
	ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string) (store.WaiterResolution, error)

	// ForceReleaseSuperseded drops superseded=1 holders so a stuck
	// CancelOthers eviction can't block forward progress.
	ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error)
}

// LocalBackends builds a Backends bundle over a local SQLite store
// and on-disk log files. Caller owns the Store lifecycle.
func LocalBackends(paths Paths, st *store.Store) Backends {
	return Backends{
		State:       localState{st: st},
		Logs:        localLogs{paths: paths},
		Concurrency: localConcurrency{st: st},
	}
}

// --- local implementations ---

type localState struct {
	st *store.Store
}

func (l localState) CreateRun(ctx context.Context, r store.Run) error {
	return l.st.CreateRun(ctx, r)
}
func (l localState) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	return l.st.FinishRun(ctx, runID, status, errMsg)
}
func (l localState) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	return l.st.UpdatePlanSnapshot(ctx, runID, snapshot)
}
func (l localState) CreateNode(ctx context.Context, n store.Node) error {
	return l.st.CreateNode(ctx, n)
}
func (l localState) StartNode(ctx context.Context, runID, nodeID string) error {
	return l.st.StartNode(ctx, runID, nodeID)
}
func (l localState) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return l.st.FinishNode(ctx, runID, nodeID, outcome, errMsg, output)
}
func (l localState) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	return l.st.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
}
func (l localState) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	return l.st.UpdateNodeDeps(ctx, runID, nodeID, deps)
}
func (l localState) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	return l.st.UpdateNodeActivity(ctx, runID, nodeID, detail)
}
func (l localState) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	return l.st.TouchNodeHeartbeat(ctx, runID, nodeID)
}
func (l localState) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error {
	return l.st.AddNodeMetricSample(ctx, runID, nodeID, sample)
}
func (l localState) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error) {
	return l.st.GetLatestRun(ctx, pipeline, statuses, maxAge)
}
func (l localState) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	n, err := l.st.GetNode(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	return n.Output, nil
}
func (l localState) GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error) {
	return l.st.GetNode(ctx, runID, nodeID)
}
func (l localState) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	return l.st.GetRun(ctx, runID)
}
func (l localState) WriteNodeDispatch(ctx context.Context, d store.NodeDispatch) error {
	return l.st.WriteNodeDispatch(ctx, d)
}
func (l localState) GetNodeDispatch(ctx context.Context, runID, nodeID string, seq int) (*store.NodeDispatch, error) {
	return l.st.GetNodeDispatch(ctx, runID, nodeID, seq)
}
func (l localState) ListNodeDispatches(ctx context.Context, runID, nodeID string) ([]*store.NodeDispatch, error) {
	return l.st.ListNodeDispatches(ctx, runID, nodeID)
}
func (l localState) CreateDebugPause(ctx context.Context, p store.DebugPause) error {
	return l.st.CreateDebugPause(ctx, p)
}
func (l localState) GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*store.DebugPause, error) {
	return l.st.GetActiveDebugPause(ctx, runID, nodeID)
}
func (l localState) ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error {
	return l.st.ReleaseDebugPause(ctx, runID, nodeID, releasedBy, kind)
}
func (l localState) ListDebugPauses(ctx context.Context, runID string) ([]*store.DebugPause, error) {
	return l.st.ListDebugPauses(ctx, runID)
}
func (l localState) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	return l.st.SetNodeStatus(ctx, runID, nodeID, status)
}
func (l localState) CreateApproval(ctx context.Context, a store.Approval) error {
	return l.st.CreateApproval(ctx, a)
}
func (l localState) GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error) {
	return l.st.GetApproval(ctx, runID, nodeID)
}
func (l localState) ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*store.Approval, error) {
	return l.st.ResolveApproval(ctx, runID, nodeID, resolution, approver, comment)
}
func (l localState) ListPendingApprovals(ctx context.Context) ([]*store.Approval, error) {
	return l.st.ListPendingApprovals(ctx)
}

func (l localState) FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error) {
	return l.st.FindSpawnedChildTriggerID(ctx, parentRunID, parentNodeID, pipeline)
}

func (l localState) EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string) (string, error) {
	if pipeline == "" {
		return "", errors.New("EnqueueTrigger: pipeline required")
	}
	// Walk ancestors and reject cycles, matching the controller.
	if parentRunID != "" {
		ancestors, err := l.st.GetRunAncestorPipelines(ctx, parentRunID)
		if err != nil {
			return "", fmt.Errorf("ancestor walk: %w", err)
		}
		parent, err := l.st.GetRun(ctx, parentRunID)
		if err != nil {
			return "", fmt.Errorf("get parent run: %w", err)
		}
		chain := append([]string{parent.Pipeline}, ancestors...)
		for _, p := range chain {
			if p == pipeline {
				return "", fmt.Errorf("cycle: %s would re-enter itself", pipeline)
			}
		}
	}
	runID := localNewRunID()
	// Cross-repo: leave SHA empty so the runner clones branch tip.
	// Same-repo: inherit parent git context.
	tg := store.Trigger{
		ID:            runID,
		Pipeline:      pipeline,
		Args:          args,
		TriggerSource: firstNonEmptyStr(source, "await-pipeline"),
		TriggerUser:   user,
		CreatedAt:     time.Now(),
		ParentRunID:   parentRunID,
		ParentNodeID:  parentNodeID,
		RetryOf:       retryOf,
	}
	if repo != "" {
		tg.Repo = repo
		tg.GitBranch = firstNonEmptyStr(branch, "main")
		owner, name := sparkwingGithubSplit(repo)
		tg.GithubOwner = owner
		tg.GithubRepo = name
	} else if parentRunID != "" {
		parent, err := l.st.GetRun(ctx, parentRunID)
		if err == nil && parent != nil {
			tg.Repo = parent.Repo
			tg.RepoURL = parent.RepoURL
			tg.GitBranch = firstNonEmptyStr(branch, parent.GitBranch)
			tg.GitSHA = parent.GitSHA
			tg.GithubOwner = parent.GithubOwner
			tg.GithubRepo = parent.GithubRepo
		}
	}
	if err := l.st.CreateTrigger(ctx, tg); err != nil {
		return "", err
	}
	return runID, nil
}

// sparkwingGithubSplit returns owner+repo from "owner/repo".
func sparkwingGithubSplit(slug string) (owner, repo string) {
	if slug == "" {
		return "", ""
	}
	for i := range len(slug) {
		if slug[i] == '/' {
			if i == 0 || i == len(slug)-1 {
				return "", ""
			}
			return slug[:i], slug[i+1:]
		}
	}
	return "", ""
}

// localNewRunID matches controller.newRunID.
func localNewRunID() string {
	return fmt.Sprintf("run-%s-%08x", time.Now().UTC().Format("20060102-150405"), time.Now().UnixNano()&0xFFFFFFFF)
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (l localState) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	_, err := l.st.AppendEvent(ctx, runID, nodeID, kind, payload)
	return err
}

type localLogs struct {
	paths Paths
}

func (l localLogs) OpenNodeLog(runID, nodeID string, delegate sparkwing.Logger) (NodeLog, error) {
	// Idempotent; safe even when callers skip Run().
	if err := os.MkdirAll(l.paths.RunDir(runID), 0o755); err != nil {
		return nil, err
	}
	return newNodeLogger(l.paths.NodeLog(runID, nodeID), nodeID, delegate)
}

// localConcurrency delegates straight to the Store. Release runs the
// promote/coalesce phases so pending arrivals unblock before return.
type localConcurrency struct {
	st *store.Store
}

func (l localConcurrency) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	return l.st.AcquireConcurrencySlot(ctx, req)
}

func (l localConcurrency) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	return l.st.HeartbeatConcurrencySlot(ctx, key, holderID, lease)
}

func (l localConcurrency) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	// All three phases (delete, drain, promote) in one txn so a crash
	// between them can't strand downstream callers.
	_, _, _, err := l.st.ReleaseAndNotify(ctx, key, holderID, outcome, outputRef, cacheKeyHash, ttl, store.DefaultConcurrencyLease)
	return err
}

func (l localConcurrency) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string) (store.WaiterResolution, error) {
	return l.st.ResolveWaiter(ctx, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID)
}

func (l localConcurrency) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	dropped, err := l.st.ForceReleaseSupersededHolders(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(dropped) > 0 {
		if _, err := l.st.PromoteNextWaiters(ctx, key, store.DefaultConcurrencyLease); err != nil {
			return dropped, fmt.Errorf("force-release: promote: %w", err)
		}
	}
	return dropped, nil
}
