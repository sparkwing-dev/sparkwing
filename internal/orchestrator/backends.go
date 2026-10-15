package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Backends struct {
	State       StateBackend
	Logs        LogBackend
	Concurrency ConcurrencyBackend

	// DiskRoot is a directory on the volume this run's own storage sits on,
	// set only where that storage is local. A run backed by an object store
	// or a remote controller leaves it empty, because this machine's free
	// bytes describe no disk that run is writing to.
	DiskRoot string

	// LocalCoordination marks state that is this machine's own runs store,
	// so this process dispatches the run's child triggers itself and owns
	// the host capacity profile. A hosted controller runs both of those on
	// its own side.
	LocalCoordination bool

	// APISocket is the admission daemon's controller API socket when this
	// run's state lives behind it, and empty when the run holds the store
	// itself. Node subprocesses dial it instead of a loopback controller.
	APISocket string
}

// RunCoordination handles child dispatch and resource observations for a run.
type RunCoordination interface {
	ListNodes(ctx context.Context, runID string) ([]*store.Node, error)
	ListNodeMetrics(ctx context.Context, runID, nodeID string) ([]store.MetricSample, error)
	AddNodeUsage(ctx context.Context, runID, nodeID string, u store.NodeUsage) error

	ListPendingTriggersForParent(ctx context.Context, parentRunID string) ([]string, error)
	ClaimSpecificTrigger(ctx context.Context, id string, lease time.Duration) (*store.Trigger, error)
	FinishTrigger(ctx context.Context, id string) error
	GetTrigger(ctx context.Context, id string) (*store.Trigger, error)

	GetPipelineProfile(ctx context.Context, pipeline, nodeID string) (*store.PipelineProfile, error)
	SetPipelinePin(ctx context.Context, pipeline, nodeID string, cores float64, memoryBytes int64) error
	RecordProfileObservation(ctx context.Context, pipeline, nodeID string, obs store.ProfileObservation) error
	RecordContention(ctx context.Context, pipeline string) error
	RecordWaitObservation(ctx context.Context, pipeline string, wait time.Duration) error
}

type StateBackend interface {
	storage.StateStoreOld
	RunCoordination

	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)

	EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string) (runID string, err error)
}

type LogBackend interface {
	OpenNodeLog(ctx context.Context, runID, nodeID string, delegate sparkwing.Logger) (NodeLog, error)
}

type NodeLog interface {
	sparkwing.Logger
	Close() error
}

type ConcurrencyBackend interface {
	AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error)
	State(ctx context.Context, key string) (*store.ConcurrencyState, error)
	ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error)
	HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (expires time.Time, superseded bool, err error)
	ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error
	ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error)

	ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error)

	CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error)
}

func LocalBackends(paths Paths, st *store.Store) Backends {
	return Backends{
		State:             localState{st: st},
		Logs:              localLogs{paths: paths},
		Concurrency:       localConcurrency{st: st},
		DiskRoot:          paths.Root,
		LocalCoordination: true,
	}
}

var _ StateBackend = (*client.Client)(nil)

func localRunLogDir(b LogBackend, runID string) string {
	switch l := b.(type) {
	case localLogs:
		return EnsureRunLogDir(l.paths, runID)
	case *localLogs:
		return EnsureRunLogDir(l.paths, runID)
	}
	return ""
}

func EnsureRunLogDir(p Paths, runID string) string {
	if err := p.EnsureRunDir(runID); err != nil {
		return ""
	}
	return absExistingDir(p.RunDir(runID))
}

func absExistingDir(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return ""
	}
	return abs
}

type localState struct {
	st *store.Store
}

func (l localState) Close() error { return l.st.Close() }

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

func (l localState) AcknowledgeNodeExecutionStart(ctx context.Context, runID, nodeID string, start store.ExecutionStart) error {
	fence, _ := store.NodeClaimFenceFromContext(ctx)
	return l.st.AcknowledgeNodeExecutionStart(ctx, runID, nodeID, fence.Claimant, start)
}

func (l localState) FinishNodeExecutionAttempt(ctx context.Context, runID, nodeID string, finish store.ExecutionAttemptFinish) error {
	fence, _ := store.NodeClaimFenceFromContext(ctx)
	return l.st.FinishNodeExecutionAttempt(ctx, runID, nodeID, fence.Claimant, finish)
}

func (l localState) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	return l.st.UpdateNodeDeps(ctx, runID, nodeID, deps)
}

func (l localState) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	return l.st.UpdateNodeActivity(ctx, runID, nodeID, detail)
}

func (l localState) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	return l.st.AppendNodeAnnotation(ctx, runID, nodeID, msg)
}

func (l localState) SetNodeSummary(ctx context.Context, runID, nodeID, md string) error {
	return l.st.SetNodeSummary(ctx, runID, nodeID, md)
}

func (l localState) SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error {
	return l.st.SetStepSummary(ctx, runID, nodeID, stepID, md)
}

func (l localState) StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return l.st.StartNodeStep(ctx, runID, nodeID, stepID)
}

func (l localState) FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error {
	return l.st.FinishNodeStep(ctx, runID, nodeID, stepID, status)
}

func (l localState) SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return l.st.SkipNodeStep(ctx, runID, nodeID, stepID)
}

func (l localState) AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error {
	return l.st.AppendStepAnnotation(ctx, runID, nodeID, stepID, msg)
}

func (l localState) ListNodeSteps(ctx context.Context, runID string) ([]*store.NodeStep, error) {
	return l.st.ListNodeSteps(ctx, runID)
}

func (l localState) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	return l.st.TouchNodeHeartbeat(ctx, runID, nodeID)
}

func (l localState) TouchRunHeartbeat(ctx context.Context, runID string) error {
	return l.st.TouchRunHeartbeat(ctx, runID)
}

func (l localState) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error {
	return l.st.AddNodeMetricSample(ctx, runID, nodeID, sample)
}

func (l localState) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error) {
	return l.st.GetLatestRun(ctx, pipeline, statuses, maxAge)
}

func (l localState) FinishNodeCopyingOutput(ctx context.Context, runID, nodeID, outcome, reason string, src copiedOutput) error {
	return l.st.FinishNodeCopyingOutput(ctx, runID, nodeID, outcome, reason, src.runID, src.nodeID)
}

func (l localState) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	return l.st.GetNodeOutput(ctx, runID, nodeID)
}

func (l localState) GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error) {
	return l.st.GetNode(ctx, runID, nodeID)
}

func (l localState) ListNodes(ctx context.Context, runID string) ([]*store.Node, error) {
	return l.st.ListNodes(ctx, runID)
}

func (l localState) SetNodeArtifactManifest(ctx context.Context, runID, nodeID, manifestDigest string) error {
	return l.st.SetNodeArtifactManifest(ctx, runID, nodeID, manifestDigest)
}

func (l localState) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	return l.st.GetRun(ctx, runID)
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

func (l localState) EnqueueTrigger(
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
) (string, error) {
	return l.EnqueueTriggerWithEnv(
		ctx, pipeline, args, parentRunID, parentNodeID,
		retryOf, source, user, repo, branch, nil,
	)
}

func (l localState) EnqueueTriggerWithEnv(
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
	if pipeline == "" {
		return "", errors.New("EnqueueTrigger: pipeline required")
	}
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
				return "", fmt.Errorf("%w: %s would re-enter itself", errTriggerCycle, pipeline)
			}
		}
	}
	runID := localNewRunID()
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
		TriggerEnv:    triggerEnv,
		RepoInherited: repo == "" && parentRunID != "",
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
			if strings.HasPrefix(parent.TriggerSource, "pipeline-working-tree@") {
				tg.TriggerSource = parent.TriggerSource
			}
			tg.Repo = parent.DeclaredRepo
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

var errTriggerCycle = errors.New("cycle")

type triggerEnqueuerWithEnv interface {
	EnqueueTriggerWithEnv(
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
	) (string, error)
}

func enqueueTriggerWithEnv(
	ctx context.Context,
	state StateBackend,
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
	if withEnv, ok := state.(triggerEnqueuerWithEnv); ok {
		return withEnv.EnqueueTriggerWithEnv(
			ctx, pipeline, args, parentRunID, parentNodeID,
			retryOf, source, user, repo, branch, triggerEnv,
		)
	}
	if len(triggerEnv) > 0 {
		return "", errors.New("EnqueueTriggerWithEnv: state backend cannot persist trigger env")
	}
	return state.EnqueueTrigger(ctx, pipeline, args, parentRunID, parentNodeID, retryOf, source, user, repo, branch)
}

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

func localNewRunID() string {
	return newRunID()
}

func NewLocalRunID() string { return localNewRunID() }

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (l localState) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	return l.st.AppendEvent(ctx, runID, nodeID, kind, payload)
}

func (l localState) ListNodeMetrics(ctx context.Context, runID, nodeID string) ([]store.MetricSample, error) {
	return l.st.ListNodeMetrics(ctx, runID, nodeID)
}

func (l localState) AddNodeUsage(ctx context.Context, runID, nodeID string, u store.NodeUsage) error {
	return l.st.AddNodeUsage(ctx, runID, nodeID, u)
}

func (l localState) ListPendingTriggersForParent(ctx context.Context, parentRunID string) ([]string, error) {
	return l.st.ListPendingTriggersForParent(ctx, parentRunID)
}

func (l localState) ClaimSpecificTrigger(ctx context.Context, id string, lease time.Duration) (*store.Trigger, error) {
	return l.st.ClaimSpecificTrigger(ctx, id, lease)
}

func (l localState) FinishTrigger(ctx context.Context, id string) error {
	return l.st.FinishTrigger(ctx, id)
}

func (l localState) GetTrigger(ctx context.Context, id string) (*store.Trigger, error) {
	return l.st.GetTrigger(ctx, id)
}

func (l localState) ReleaseClaimAtGeneration(ctx context.Context, id string, seq int64) (bool, error) {
	return l.st.ReleaseClaimAtGeneration(ctx, id, seq)
}

func (l localState) GetPipelineProfile(ctx context.Context, pipeline, nodeID string) (*store.PipelineProfile, error) {
	return l.st.GetPipelineProfile(ctx, pipeline, nodeID)
}

func (l localState) SetPipelinePin(ctx context.Context, pipeline, nodeID string, cores float64, memoryBytes int64) error {
	// safety: mirrors the controller route, so a pin lands in the same row
	// whether the run writes it here or over the wire.
	if cores <= 0 && memoryBytes <= 0 {
		return l.st.SetProfilePin(ctx, pipeline, nodeID, 0, 0)
	}
	return l.st.UpsertProfilePin(ctx, pipeline, nodeID, cores, memoryBytes)
}

func (l localState) RecordProfileObservation(ctx context.Context, pipeline, nodeID string, obs store.ProfileObservation) error {
	return l.st.RecordProfileObservation(ctx, pipeline, nodeID, obs)
}

func (l localState) RecordContention(ctx context.Context, pipeline string) error {
	return l.st.RecordContention(ctx, pipeline)
}

func (l localState) RecordWaitObservation(ctx context.Context, pipeline string, wait time.Duration) error {
	return l.st.RecordWaitObservation(ctx, pipeline, wait)
}

type localLogs struct {
	paths Paths
}

func (l localLogs) OpenNodeLog(_ context.Context, runID, nodeID string, delegate sparkwing.Logger) (NodeLog, error) {
	if err := l.paths.EnsureRunDir(runID); err != nil {
		return nil, err
	}
	return newNodeLogger(l.paths.NodeLog(runID, nodeID), nodeID, delegate)
}

type localConcurrency struct {
	st *store.Store
}

func (l localConcurrency) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	return l.st.AcquireConcurrencySlot(ctx, req)
}

func (l localConcurrency) State(ctx context.Context, key string) (*store.ConcurrencyState, error) {
	return l.st.GetConcurrencyState(ctx, key)
}

func (l localConcurrency) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	return l.st.HeartbeatConcurrencySlot(ctx, key, holderID, lease)
}

func (l localConcurrency) ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error) {
	return l.st.ConcurrencyHolder(ctx, key, holderID, time.Now())
}

func (l localConcurrency) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	_, _, _, err := l.st.ReleaseAndNotify(ctx, key, holderID, outcome, outputRef, cacheKeyHash, ttl, store.DefaultConcurrencyLease)
	return err
}

func (l localConcurrency) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error) {
	return l.st.ResolveWaiter(ctx, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID, bypassRead)
}

func (l localConcurrency) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	return l.st.CancelWaiter(ctx, key, runID, nodeID)
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
