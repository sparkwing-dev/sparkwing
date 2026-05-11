package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestBackendsSeam_DrivesAllInterfaces runs a tiny pipeline through
// fake backends and asserts that the orchestrator writes run/node
// state, appends events, requests log sinks, and acquires exclusive
// locks via the Backends interface (never reaches for the local
// store directly). If a future refactor accidentally hardwires to
// *store.Store again, this test fails.
func TestBackendsSeam_DrivesAllInterfaces(t *testing.T) {
	register("seam-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return seamOK{} })

	fakes := newFakeBackends()
	res, err := orchestrator.Run(context.Background(),
		orchestrator.Backends{State: fakes.state, Logs: fakes.logs, Concurrency: fakes.concurrency},
		orchestrator.Options{Pipeline: "seam-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status=%q want success; err=%v", res.Status, res.Error)
	}

	// CreateRun + FinishRun -- must cover the lifecycle.
	if fakes.state.createRuns != 1 {
		t.Errorf("CreateRun called %d times; want 1", fakes.state.createRuns)
	}
	if fakes.state.finishRuns != 1 {
		t.Errorf("FinishRun called %d times; want 1", fakes.state.finishRuns)
	}

	// Two nodes -> two CreateNode, two StartNode, two FinishNode.
	if fakes.state.createNodes != 2 {
		t.Errorf("CreateNode called %d times; want 2", fakes.state.createNodes)
	}
	if fakes.state.startNodes != 2 {
		t.Errorf("StartNode called %d times; want 2", fakes.state.startNodes)
	}
	if fakes.state.finishNodes != 2 {
		t.Errorf("FinishNode called %d times; want 2", fakes.state.finishNodes)
	}

	// Node events: _started + _succeeded for each of two nodes.
	if got := fakes.state.eventKinds["node_started"]; got != 2 {
		t.Errorf("node_started events=%d; want 2", got)
	}
	if got := fakes.state.eventKinds["node_succeeded"]; got != 2 {
		t.Errorf("node_succeeded events=%d; want 2", got)
	}

	// Each node gets its own log sink.
	if fakes.logs.opened != 2 {
		t.Errorf("OpenNodeLog called %d times; want 2", fakes.logs.opened)
	}

	// The .Cache()-declared node -- exactly one -- acquires a slot.
	if fakes.concurrency.acquires != 1 {
		t.Errorf("Concurrency.AcquireSlot called %d times; want 1", fakes.concurrency.acquires)
	}
	if fakes.concurrency.releases != 1 {
		t.Errorf("Concurrency.ReleaseSlot called %d times; want 1", fakes.concurrency.releases)
	}
}

// TestBackendsSeam_StateErrorPropagates ensures a failing
// CreateRun surfaces to the caller without silent success.
func TestBackendsSeam_StateErrorPropagates(t *testing.T) {
	register("seam-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return seamOK{} })

	fakes := newFakeBackends()
	fakes.state.createRunErr = errors.New("db down")

	_, err := orchestrator.Run(context.Background(),
		orchestrator.Backends{State: fakes.state, Logs: fakes.logs, Concurrency: fakes.concurrency},
		orchestrator.Options{Pipeline: "seam-ok"})
	if err == nil || !errIs(err, "db down") {
		t.Fatalf("want error containing 'db down'; got %v", err)
	}
}

// --- fixture pipeline ---

type seamOK struct{ sparkwing.Base }

func (seamOK) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	a := sparkwing.Job(plan, "a", func(ctx context.Context) error { return nil })
	sparkwing.Job(plan, "b", func(ctx context.Context) error { return nil }).
		Needs(a).Cache(sparkwing.CacheOptions{Key: "seam-lock"})
	return nil
}

// --- fake backends ---

type fakeBackends struct {
	state       *fakeState
	logs        *fakeLogs
	concurrency *fakeConcurrency
}

func newFakeBackends() *fakeBackends {
	return &fakeBackends{
		state:       &fakeState{eventKinds: map[string]int{}, cache: map[string][]byte{}},
		logs:        &fakeLogs{},
		concurrency: &fakeConcurrency{},
	}
}

type fakeState struct {
	mu           sync.Mutex
	createRuns   int
	finishRuns   int
	createNodes  int
	startNodes   int
	finishNodes  int
	eventKinds   map[string]int
	cache        map[string][]byte
	createRunErr error
}

func (f *fakeState) CreateRun(ctx context.Context, r store.Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createRunErr != nil {
		return f.createRunErr
	}
	f.createRuns++
	return nil
}
func (f *fakeState) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishRuns++
	return nil
}
func (f *fakeState) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	return nil
}
func (f *fakeState) CreateNode(ctx context.Context, n store.Node) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createNodes++
	return nil
}
func (f *fakeState) StartNode(ctx context.Context, runID, nodeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startNodes++
	return nil
}
func (f *fakeState) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishNodes++
	return nil
}
func (f *fakeState) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishNodes++
	return nil
}
func (f *fakeState) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	return nil
}
func (f *fakeState) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	return nil
}
func (f *fakeState) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	return nil
}
func (f *fakeState) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	return nil
}
func (f *fakeState) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventKinds[kind]++
	return nil
}
func (f *fakeState) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error {
	return nil
}
func (f *fakeState) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error) {
	return nil, store.ErrNotFound
}
func (f *fakeState) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	return nil, store.ErrNotFound
}
func (f *fakeState) GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error) {
	return nil, store.ErrNotFound
}
func (f *fakeState) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	return nil, store.ErrNotFound
}
func (f *fakeState) EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string) (string, error) {
	return "", nil
}
func (f *fakeState) FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error) {
	return "", nil
}
func (f *fakeState) CreateDebugPause(ctx context.Context, p store.DebugPause) error {
	return nil
}
func (f *fakeState) GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*store.DebugPause, error) {
	return nil, store.ErrNotFound
}
func (f *fakeState) ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error {
	return nil
}
func (f *fakeState) ListDebugPauses(ctx context.Context, runID string) ([]*store.DebugPause, error) {
	return nil, nil
}
func (f *fakeState) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	return nil
}
func (f *fakeState) CreateApproval(ctx context.Context, a store.Approval) error {
	return nil
}
func (f *fakeState) GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error) {
	return nil, store.ErrNotFound
}
func (f *fakeState) ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*store.Approval, error) {
	return nil, store.ErrNotFound
}
func (f *fakeState) ListPendingApprovals(ctx context.Context) ([]*store.Approval, error) {
	return nil, nil
}
func (f *fakeState) WriteNodeDispatch(ctx context.Context, d store.NodeDispatch) error {
	return nil
}
func (f *fakeState) GetNodeDispatch(ctx context.Context, runID, nodeID string, seq int) (*store.NodeDispatch, error) {
	return nil, store.ErrNotFound
}
func (f *fakeState) ListNodeDispatches(ctx context.Context, runID, nodeID string) ([]*store.NodeDispatch, error) {
	return nil, nil
}

type fakeLogs struct {
	mu     sync.Mutex
	opened int
}

func (f *fakeLogs) OpenNodeLog(runID, nodeID string, delegate sparkwing.Logger) (orchestrator.NodeLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened++
	return &discardLog{}, nil
}

type discardLog struct{}

func (*discardLog) Log(level, msg string)    {}
func (*discardLog) Emit(sparkwing.LogRecord) {}
func (*discardLog) Close() error             { return nil }

type fakeConcurrency struct {
	mu       sync.Mutex
	acquires int
	releases int
}

func (f *fakeConcurrency) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	f.mu.Lock()
	f.acquires++
	f.mu.Unlock()
	return store.AcquireSlotResponse{
		Kind:           store.AcquireGranted,
		HolderID:       req.HolderID,
		LeaseExpiresAt: time.Now().Add(30 * time.Second),
	}, nil
}

func (f *fakeConcurrency) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	return time.Now().Add(lease), false, nil
}

func (f *fakeConcurrency) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	f.mu.Lock()
	f.releases++
	f.mu.Unlock()
	return nil
}

func (f *fakeConcurrency) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string) (store.WaiterResolution, error) {
	return store.WaiterResolution{Status: store.WaiterStillWaiting}, nil
}

func (f *fakeConcurrency) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	return nil, nil
}

func errIs(err error, substr string) bool {
	return err != nil && (err.Error() == substr || containsStr(err.Error(), substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
