package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Backends bundles the infrastructure interfaces the orchestrator
// depends on.
type Backends struct {
	State       StateBackend
	Logs        LogBackend
	Concurrency ConcurrencyBackend
}

// StateBackend persists run/node/event/cache state. The orchestrator
// holds it in Backends.State. It embeds storage.StateStore (the
// methods every state-store implementation must expose) and adds the
// wrapper-shaped methods that fold adapter logic on top of the raw
// store (output extraction, trigger cycle detection, simplified-error
// AppendEvent).
type StateBackend interface {
	storage.StateStore

	// AppendEvent mirrors store.AppendEvent but discards the sequence
	// number; orchestrator call sites never read it.
	AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error

	// GetNodeOutput returns a finished node's raw JSON output.
	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)

	// EnqueueTrigger spawns a new trigger; cycles are rejected with
	// a wrapped error mentioning "cycle". parentNodeID + retryOf
	// thread retry lineage across nested spawns.
	EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string) (runID string, err error)
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

// ConcurrencyBackend mediates the Cache()/Concurrency() DSL: atomic
// acquire (granted/skipped/failed/cached/queued/coalesced), waiter
// resolution, memoizing release (which also promotes waiters), and
// heartbeats that surface the supersede signal.
type ConcurrencyBackend interface {
	AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error)
	HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (expires time.Time, superseded bool, err error)
	ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error
	ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error)

	// ForceReleaseSuperseded drops superseded=1 holders so a stuck
	// CancelOthers eviction can't block forward progress.
	ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error)

	// CancelWaiter removes a parked waiter row so it won't later be
	// promoted to a holder. The QueueTimeout path uses it to clean up
	// after a waiter that gave up. Reports whether a row matched.
	CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error)
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

// S3Backends builds a Backends bundle for Mode 2 (S3-only shared).
// State is the NDJSON-over-object-store backend; Logs is the
// supplied storage.LogStore wrapped as a LogBackend; Concurrency is
// the no-op backend (no cross-runner cache reservation). Caller
// owns the s3state.Backend lifecycle.
func S3Backends(log storage.LogStore, state *s3state.Backend) Backends {
	return Backends{
		State:       s3StateAdapter{Backend: state},
		Logs:        NewLogStoreBackend(log, nil),
		Concurrency: &noopConcurrency{},
	}
}

// s3StateAdapter wraps *s3state.Backend so it satisfies StateBackend.
// AppendEvent + GetNodeOutput are real implementations on the
// embedded backend; EnqueueTrigger surfaces ErrNotSupported because
// triggers require a central rendezvous Mode 2 deliberately omits.
type s3StateAdapter struct {
	*s3state.Backend
}

func (s s3StateAdapter) EnqueueTrigger(_ context.Context, _ string, _ map[string]string, _, _, _, _, _, _, _ string) (string, error) {
	return "", fmt.Errorf("%w: pipeline triggers require Mode 3 (Postgres) or Mode 4 (hosted controller)", s3state.ErrNotSupported)
}

// Compile-time check: *client.Client (the HTTP-backed state surface
// the cluster worker and Mode 4 laptop runs against) satisfies the
// orchestrator's runtime StateBackend interface. The narrower
// storage.StateStore assertion lives next to the client itself in
// pkg/controller/client/state_assertion.go; this one catches drift in
// the orchestrator-only adapter methods (AppendEvent, GetNodeOutput,
// EnqueueTrigger).
var _ StateBackend = (*client.Client)(nil)

// RemoteBackends builds a Backends bundle for Mode 4 (hosted
// controller). State + concurrency talk to the same controller HTTP
// surface; logs is the caller-supplied LogBackend or, when nil, a
// fresh HTTP logs backend pointed at the same controller. httpClient
// shapes the concurrency backend's transport; nil picks the default.
// The lease argument shapes how long the HTTPConcurrency holders run
// before the controller can reap them; zero falls back to the package
// default.
//
// Use this when state-store creds are an HTTP profile, not direct
// access to a database. The laptop path, the cluster worker, and the
// single-node runner all assemble Mode 4 through this one constructor,
// symmetric with LocalBackends + S3Backends.
func RemoteBackends(c *client.Client, logs LogBackend, httpClient *http.Client, lease time.Duration) Backends {
	if logs == nil {
		logs = NewHTTPLogsWithToken(c.BaseURL(), nil, c.Token(), nil)
	}
	if httpClient == nil {
		httpClient = defaultHTTPClient()
	}
	return Backends{
		State:       c,
		Logs:        logs,
		Concurrency: NewHTTPConcurrency(c.BaseURL(), httpClient, c.Token(), lease),
	}
}

// defaultHTTPClient returns nil so NewHTTPConcurrency picks its own
// default transport (mirrors how client.NewWithToken handles a nil
// httpClient). Kept as a named helper so a future change to
// "share the same *http.Client across state + concurrency" lands in
// one place.
func defaultHTTPClient() *http.Client { return nil }

// canonicalLocalStore returns the *store.Store underneath a StateBackend
// when one is reachable -- either a bare localState, or one nested inside
// a mirrorStateBackend's canonical slot. Cluster-backed (controller / S3)
// state returns nil. The local-trigger dispatcher uses this to find the
// store whose triggers table it should poll, regardless of whether the
// caller is sqlite, postgres, or postgres+mirror.
func canonicalLocalStore(b StateBackend) *store.Store {
	switch s := b.(type) {
	case localState:
		return s.st
	case *mirrorStateBackend:
		return canonicalLocalStore(s.canonical)
	}
	return nil
}

type localState struct {
	st *store.Store
}

// Close satisfies storage.StateStore. The orchestrator never invokes
// Close through Backends.State; RunLocal owns the underlying store
// lifecycle and closes it directly. The method exists so localState
// satisfies the storage.StateStore interface.
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
