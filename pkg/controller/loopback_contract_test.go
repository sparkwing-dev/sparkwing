package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type memArt struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemArt() *memArt { return &memArt{data: map[string][]byte{}} }

func (m *memArt) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memArt) Put(_ context.Context, key string, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = body
	return nil
}

func (m *memArt) Has(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memArt) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memArt) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

type s3Adapter struct{ *s3state.Backend }

const loopbackToken = "swl_contract"

const contractRunID = "run-contract"

func TestLoopbackContract_EveryRouteTheNodeClientCalls(t *testing.T) {
	t.Parallel()
	art := newMemArt()
	backend := s3state.New(art, s3state.WithFlushInterval(10*time.Millisecond))
	t.Cleanup(func() { _ = backend.Close() })

	c, _ := newLoopbackClient(t, s3Adapter{Backend: backend}, contractRunID, nil, art)
	runContractSurface(t, c, "s3")

	if err := backend.Close(); err != nil {
		t.Fatalf("close backend: %v", err)
	}
	if has, _ := art.Has(context.Background(), "runs/run-contract/state.ndjson"); !has {
		t.Error("no state.ndjson landed in the bucket for run-contract")
	}
}

func TestLoopbackContract_MatchesTheRealController(t *testing.T) {
	t.Parallel()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := httptest.NewServer(controller.New(st, quietLogger()).Handler())
	t.Cleanup(srv.Close)

	runContractSurface(t, client.NewWithToken(srv.URL, nil, ""), "sqlite")
}

func TestLoopbackGetNodeUsesCanonicalPublicProjection(t *testing.T) {
	art := newMemArt()
	backend := s3state.New(art)
	t.Cleanup(func() { _ = backend.Close() })
	ctx := context.Background()
	if err := backend.CreateRun(ctx, store.Run{ID: contractRunID, Pipeline: "demo", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	node := &store.Node{
		RunID: contractRunID, NodeID: "build", Status: "running",
		ClaimedBy: "private-holder", ClaimWorkerID: "desktop-public", ClaimExecutorKind: "agent",
		ClaimReservationID: "private-claim-reservation", CoordinatorID: "private-coordinator",
		ClaimGeneration: 7, ClaimMembershipID: "private-membership", ExecutorKind: "agent",
		ExecutorID: "private-executor", ExecutorLocation: "local",
		RequiredCoordinatorID: "private-required-coordinator", ReservationID: "private-reservation",
		ExecutionAttempts: []store.ExecutionAttempt{{
			RunID: contractRunID, NodeID: "build", Attempt: 1, ClaimGeneration: 7,
			ExecutorKind: "agent", ExecutorName: "desktop-public", ExecutorLocation: "local",
			StartedAt: time.Now().UTC(), RetryRunID: "retry-public",
		}},
	}
	if err := backend.CreateNode(ctx, *node); err != nil {
		t.Fatal(err)
	}
	_, srv := newLoopbackClient(t, s3Adapter{Backend: backend}, contractRunID, nil, art)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/runs/"+contractRunID+"/nodes/build", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+loopbackToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(api.PublicNode(node))
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if !bytes.Equal(body, want) {
		t.Fatalf("loopback node differs from canonical projection\n got: %s\nwant: %s", body, want)
	}
	for _, private := range []string{
		"private-holder", "private-claim-reservation", "private-coordinator", "private-membership",
		"private-executor", "private-required-coordinator", "private-reservation", `"claim_generation"`,
	} {
		if bytes.Contains(body, []byte(private)) {
			t.Errorf("loopback node exposed %q: %s", private, body)
		}
	}
	if !bytes.Contains(body, []byte(`"executor_name":"desktop-public"`)) ||
		!bytes.Contains(body, []byte(`"retry_run_id":"retry-public"`)) {
		t.Errorf("loopback node omitted public attribution: %s", body)
	}
}

func newLoopbackClient(t *testing.T, state controller.LoopbackState, runID string, conc controller.LoopbackConcurrency, art storage.ArtifactStore) (*client.Client, *httptest.Server) {
	t.Helper()
	lb := controller.NewLoopback(state, runID, loopbackToken, quietLogger()).
		WithConcurrency(conc).
		WithArtifactStore(art)
	srv := httptest.NewServer(lb.Handler())
	t.Cleanup(srv.Close)
	return client.NewWithToken(srv.URL, nil, loopbackToken), srv
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func runContractSurface(t *testing.T, c *client.Client, backing string) {
	t.Helper()
	ctx := context.Background()
	const runID = contractRunID

	if err := c.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "contract", Status: "running",
		Args: map[string]string{"token": "s3cret"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := c.UpdatePlanSnapshot(ctx, runID, []byte(`{"nodes":[]}`)); err != nil {
		t.Fatalf("UpdatePlanSnapshot: %v", err)
	}

	run, err := c.GetRunForExecution(ctx, runID)
	if err != nil {
		t.Fatalf("GetRunForExecution: %v", err)
	}
	if run.Pipeline != "contract" {
		t.Errorf("GetRunForExecution pipeline = %q", run.Pipeline)
	}
	if _, err := c.GetRun(ctx, runID); err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if _, err := c.GetTrigger(ctx, runID); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetTrigger: %v", err)
	}

	if err := c.CreateNode(ctx, store.Node{RunID: runID, NodeID: "produce", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := c.UpdateNodeDeps(ctx, runID, "produce", []string{}); err != nil {
		t.Fatalf("UpdateNodeDeps: %v", err)
	}
	if err := c.StartNode(ctx, runID, "produce"); err != nil {
		t.Fatalf("StartNode: %v", err)
	}
	if err := c.SetNodeStatus(ctx, runID, "produce", "running"); err != nil {
		t.Fatalf("SetNodeStatus: %v", err)
	}
	if err := c.UpdateNodeActivity(ctx, runID, "produce", "compiling"); err != nil {
		t.Fatalf("UpdateNodeActivity: %v", err)
	}
	if err := c.TouchNodeHeartbeat(ctx, runID, "produce"); err != nil {
		t.Fatalf("TouchNodeHeartbeat: %v", err)
	}
	if err := c.TouchRunHeartbeat(ctx, runID); err != nil {
		t.Fatalf("TouchRunHeartbeat: %v", err)
	}
	if err := c.AppendNodeAnnotation(ctx, runID, "produce", "note"); err != nil {
		t.Fatalf("AppendNodeAnnotation: %v", err)
	}
	if err := c.SetNodeSummary(ctx, runID, "produce", "# done"); err != nil {
		t.Fatalf("SetNodeSummary: %v", err)
	}
	if err := c.SetNodeArtifactManifest(ctx, runID, "produce", "sha256:abc"); err != nil {
		t.Fatalf("SetNodeArtifactManifest: %v", err)
	}
	if err := c.AddNodeMetricSample(ctx, runID, "produce", store.MetricSample{
		TS: time.Now().UTC(), CPUMillicores: 250, MemoryBytes: 1 << 20, CPUTime: 40 * time.Millisecond,
	}); err != nil {
		t.Fatalf("AddNodeMetricSample: %v", err)
	}
	if err := c.AppendEvent(ctx, runID, "produce", "node_started", []byte(`{}`)); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	if err := c.StartNodeStep(ctx, runID, "produce", "run"); err != nil {
		t.Fatalf("StartNodeStep: %v", err)
	}
	if err := c.AppendStepAnnotation(ctx, runID, "produce", "run", "step note"); err != nil {
		t.Fatalf("AppendStepAnnotation: %v", err)
	}
	if err := c.SetStepSummary(ctx, runID, "produce", "run", "## step"); err != nil {
		t.Fatalf("SetStepSummary: %v", err)
	}
	if err := c.FinishNodeStep(ctx, runID, "produce", "run", store.StepPassed); err != nil {
		t.Fatalf("FinishNodeStep: %v", err)
	}
	if err := c.SkipNodeStep(ctx, runID, "produce", "skipped"); err != nil {
		t.Fatalf("SkipNodeStep: %v", err)
	}
	steps, err := c.ListNodeSteps(ctx, runID)
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	if len(steps) == 0 {
		t.Error("ListNodeSteps returned nothing after two step writes")
	}

	if err := c.FinishNodeWithReason(ctx, runID, "produce", "success", "",
		[]byte(`{"digest":"sha-abc123"}`), "", nil); err != nil {
		t.Fatalf("FinishNodeWithReason: %v", err)
	}
	out, err := c.GetNodeOutput(ctx, runID, "produce")
	if err != nil {
		t.Fatalf("GetNodeOutput: %v", err)
	}
	if string(out) != `{"digest":"sha-abc123"}` {
		t.Errorf("GetNodeOutput = %s", out)
	}
	node, err := c.GetNode(ctx, runID, "produce")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.Outcome != "success" {
		t.Errorf("node outcome = %q, want success", node.Outcome)
	}

	if err := c.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: "produce/scan", Status: "pending",
	}); err != nil {
		t.Fatalf("CreateNode(spawn child): %v", err)
	}
	if err := c.FinishNode(ctx, runID, "produce/scan", "success", "", []byte(`{"findings":7}`)); err != nil {
		t.Fatalf("FinishNode(spawn child): %v", err)
	}
	childOut, err := c.GetNodeOutput(ctx, runID, "produce/scan")
	if err != nil {
		t.Fatalf("GetNodeOutput(spawn child): %v", err)
	}
	if string(childOut) != `{"findings":7}` {
		t.Errorf("spawn child output = %s", childOut)
	}

	if err := c.WriteNodeDispatch(ctx, store.NodeDispatch{
		RunID: runID, NodeID: "produce", Seq: 1, CodeVersion: "local", DispatchedAt: time.Now().UTC(),
	}); err != nil && !isUnsupported(err) {
		t.Fatalf("WriteNodeDispatch: %v", err)
	} else if err == nil {
		if _, gerr := c.GetNodeDispatch(ctx, runID, "produce", -1); gerr != nil {
			t.Fatalf("GetNodeDispatch: %v", gerr)
		}
		if _, lerr := c.ListNodeDispatches(ctx, runID, "produce"); lerr != nil {
			t.Fatalf("ListNodeDispatches: %v", lerr)
		}
	}

	if err := c.FinishRun(ctx, runID, "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	latest, err := c.GetLatestRun(ctx, "contract", []string{"success"}, time.Hour)
	if err != nil {
		t.Fatalf("GetLatestRun: %v", err)
	}
	if latest.ID != runID {
		t.Errorf("GetLatestRun = %q, want %q", latest.ID, runID)
	}

	if id, err := c.FindSpawnedChildTriggerID(ctx, runID, "produce", "child-pipeline"); err != nil {
		if !isUnsupported(err) {
			t.Fatalf("FindSpawnedChildTriggerID: %v", err)
		}
	} else if id != "" {
		t.Errorf("FindSpawnedChildTriggerID on a run that spawned nothing = %q", id)
	}
	childRunID, err := c.EnqueueTriggerWithEnv(ctx, "child-pipeline", map[string]string{"k": "v"},
		runID, "produce", "", "await-pipeline", "", "", "", nil)
	if err != nil && !isUnsupported(err) {
		t.Fatalf("EnqueueTriggerWithEnv: %v", err)
	}
	if err == nil {
		if childRunID == "" {
			t.Error("EnqueueTriggerWithEnv returned an empty child run id")
		}

		if id, ferr := c.FindSpawnedChildTriggerID(ctx, runID, "produce", "child-pipeline"); ferr == nil && id == "" {
			t.Errorf("FindSpawnedChildTriggerID found no child after enqueueing %q", childRunID)
		}
	}

	pauseErr := c.CreateDebugPause(ctx, store.DebugPause{
		RunID: runID, NodeID: "produce", Reason: "contract", PausedAt: time.Now().UTC(),
	})
	if pauseErr != nil && !isUnsupported(pauseErr) {
		t.Fatalf("CreateDebugPause: %v", pauseErr)
	}
	if _, err := c.ListDebugPauses(ctx, runID); err != nil && !isUnsupported(err) {
		t.Fatalf("ListDebugPauses: %v", err)
	}
	if pauseErr == nil {
		if _, err := c.GetActiveDebugPause(ctx, runID, "produce"); err != nil {
			t.Fatalf("GetActiveDebugPause: %v", err)
		}
		if err := c.ReleaseDebugPause(ctx, runID, "produce", "tester", store.PauseReleaseManual); err != nil {
			t.Fatalf("ReleaseDebugPause: %v", err)
		}
	}

	if err := c.CreateApproval(ctx, store.Approval{RunID: runID, NodeID: "gate", Message: "ok?"}); err != nil {
		if !isUnsupported(err) {
			t.Fatalf("CreateApproval: %v", err)
		}
	} else {
		if _, err := c.GetApproval(ctx, runID, "gate"); err != nil {
			t.Fatalf("GetApproval: %v", err)
		}
		if _, err := c.ResolveApproval(ctx, runID, "gate",
			store.ApprovalResolutionApproved, "tester", ""); err != nil {
			t.Fatalf("ResolveApproval: %v", err)
		}
		if _, err := c.ListPendingApprovals(ctx); err != nil {
			t.Fatalf("ListPendingApprovals: %v", err)
		}
	}

	if _, err := c.GetRun(ctx, "run-does-not-exist"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetRun(missing) on %s = %v, want store.ErrNotFound", backing, err)
	}
	if _, err := c.GetNode(ctx, runID, "no-such-node"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetNode(missing) on %s = %v, want store.ErrNotFound", backing, err)
	}
}

func isUnsupported(err error) bool {
	return errors.Is(err, storage.ErrNotSupported) ||
		strings.Contains(err.Error(), "not supported")
}

func TestLoopback_RefusesMutationsAimedAtAnotherRun(t *testing.T) {
	t.Parallel()
	art := newMemArt()

	backend := s3state.New(art, s3state.WithBufferThreshold(1))
	t.Cleanup(func() { _ = backend.Close() })

	ctx := context.Background()
	const victim = "run-victim"
	if err := backend.CreateRun(ctx, store.Run{
		ID: victim, Pipeline: "other", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed victim run: %v", err)
	}
	if err := backend.CreateNode(ctx, store.Node{RunID: victim, NodeID: "n", Status: "running"}); err != nil {
		t.Fatalf("seed victim node: %v", err)
	}

	c, _ := newLoopbackClient(t, s3Adapter{Backend: backend}, contractRunID, nil, art)
	if err := c.CreateRun(ctx, store.Run{ID: contractRunID, Pipeline: "mine", Status: "running"}); err != nil {
		t.Fatalf("CreateRun for the loopback's own run: %v", err)
	}

	refused := map[string]func() error{
		"FinishRun":      func() error { return c.FinishRun(ctx, victim, "failed", "hijacked") },
		"UpdatePlan":     func() error { return c.UpdatePlanSnapshot(ctx, victim, []byte(`{}`)) },
		"TouchRun":       func() error { return c.TouchRunHeartbeat(ctx, victim) },
		"AppendEvent":    func() error { return c.AppendEvent(ctx, victim, "n", "node_started", []byte(`{}`)) },
		"CreateNode":     func() error { return c.CreateNode(ctx, store.Node{RunID: victim, NodeID: "x", Status: "pending"}) },
		"StartNode":      func() error { return c.StartNode(ctx, victim, "n") },
		"FinishNode":     func() error { return c.FinishNode(ctx, victim, "n", "failed", "hijacked", nil) },
		"SetNodeStatus":  func() error { return c.SetNodeStatus(ctx, victim, "n", "done") },
		"NodeActivity":   func() error { return c.UpdateNodeActivity(ctx, victim, "n", "x") },
		"NodeTouch":      func() error { return c.TouchNodeHeartbeat(ctx, victim, "n") },
		"NodeAnnotation": func() error { return c.AppendNodeAnnotation(ctx, victim, "n", "x") },
		"NodeSummary":    func() error { return c.SetNodeSummary(ctx, victim, "n", "x") },
		"NodeManifest":   func() error { return c.SetNodeArtifactManifest(ctx, victim, "n", "sha256:x") },
		"NodeMetric":     func() error { return c.AddNodeMetricSample(ctx, victim, "n", store.MetricSample{TS: time.Now()}) },
		"StepStart":      func() error { return c.StartNodeStep(ctx, victim, "n", "s") },
		"StepFinish":     func() error { return c.FinishNodeStep(ctx, victim, "n", "s", store.StepFailed) },
		"StepSkip":       func() error { return c.SkipNodeStep(ctx, victim, "n", "s") },
		"StepAnnotation": func() error { return c.AppendStepAnnotation(ctx, victim, "n", "s", "x") },
		"StepSummary":    func() error { return c.SetStepSummary(ctx, victim, "n", "s", "x") },
		"NodeDeps":       func() error { return c.UpdateNodeDeps(ctx, victim, "n", []string{"a"}) },
		"ReleasePause":   func() error { return c.ReleaseDebugPause(ctx, victim, "n", "x", store.PauseReleaseManual) },
		"CreatePause": func() error {
			return c.CreateDebugPause(ctx, store.DebugPause{RunID: victim, NodeID: "n", Reason: "x"})
		},
		"CreateApproval": func() error { return c.CreateApproval(ctx, store.Approval{RunID: victim, NodeID: "n"}) },
		"ResolveApproval": func() error {
			_, err := c.ResolveApproval(ctx, victim, "n", store.ApprovalResolutionApproved, "x", "")
			return err
		},
		"WriteDispatch": func() error { return c.WriteNodeDispatch(ctx, store.NodeDispatch{RunID: victim, NodeID: "n", Seq: 1}) },
		"ForgeLineage": func() error {
			_, err := c.EnqueueTriggerWithEnv(ctx, "child", nil, victim, "n", "", "await-pipeline", "", "", "", nil)
			return err
		},
	}
	for name, call := range refused {
		if err := call(); err == nil {
			t.Errorf("%s against run %s succeeded; the loopback is scoped to %s", name, victim, contractRunID)
		} else if !strings.Contains(err.Error(), "404") && !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s against run %s = %v, want a 404", name, victim, err)
		}
	}

	n, err := backend.GetNode(ctx, victim, "n")
	if err != nil {
		t.Fatalf("read victim node: %v", err)
	}
	if n.Outcome != "" || n.Status != "running" {
		t.Errorf("victim node = status %q outcome %q, want an untouched running row", n.Status, n.Outcome)
	}
	run, err := backend.GetRun(ctx, victim)
	if err != nil {
		t.Fatalf("read victim run: %v", err)
	}
	if run.Status != "running" {
		t.Errorf("victim run status = %q, want running", run.Status)
	}

	if got, rerr := c.GetRun(ctx, victim); rerr != nil || got.Pipeline != "other" {
		t.Errorf("cross-run read = (%+v, %v), want the victim run", got, rerr)
	}
	if _, rerr := c.GetNode(ctx, victim, "n"); rerr != nil {
		t.Errorf("cross-run node read: %v", rerr)
	}
	if _, rerr := c.GetLatestRun(ctx, "other", []string{"running"}, time.Hour); rerr != nil {
		t.Errorf("cross-pipeline latest read: %v", rerr)
	}

	if err := c.CreateNode(ctx, store.Node{RunID: contractRunID, NodeID: "mine", Status: "pending"}); err != nil {
		t.Fatalf("same-run CreateNode: %v", err)
	}
	if err := c.FinishNode(ctx, contractRunID, "mine", "success", "", []byte(`{}`)); err != nil {
		t.Fatalf("same-run FinishNode: %v", err)
	}
}

type brokenState struct{ controller.LoopbackState }

func (brokenState) GetRun(context.Context, string) (*store.Run, error) {
	return nil, errors.New("bucket unreachable")
}

func TestLoopback_PlainBackendErrorIs500(t *testing.T) {
	t.Parallel()
	backend := s3state.New(newMemArt())
	t.Cleanup(func() { _ = backend.Close() })
	_, srv := newLoopbackClient(t, brokenState{s3Adapter{Backend: backend}}, contractRunID, nil, nil)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/runs/whatever", nil)
	req.Header.Set("Authorization", "Bearer "+loopbackToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := strings.TrimSpace(string(body)); got != `{"error":"internal server error"}` {
		t.Errorf("body = %s, want stable internal error", body)
	}
}

func TestLoopback_RejectsAnyOtherBearer(t *testing.T) {
	t.Parallel()
	backend := s3state.New(newMemArt())
	t.Cleanup(func() { _ = backend.Close() })
	_, srv := newLoopbackClient(t, s3Adapter{Backend: backend}, contractRunID, nil, nil)

	for name, token := range map[string]string{
		"no token":    "",
		"wrong token": "swl_not_this_run",
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/runs/anything", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, resp.StatusCode)
		}
	}

	resp, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want 200", resp.StatusCode)
	}
}

func TestLoopback_ConcurrentNodeWritesSerializeOnOneRun(t *testing.T) {
	t.Parallel()
	art := newMemArt()
	backend := s3state.New(art, s3state.WithFlushInterval(5*time.Millisecond))
	t.Cleanup(func() { _ = backend.Close() })
	c, _ := newLoopbackClient(t, s3Adapter{Backend: backend}, "run-parallel", nil, art)

	ctx := context.Background()
	const runID = "run-parallel"
	if err := c.CreateRun(ctx, store.Run{ID: runID, Pipeline: "fanout", Status: "running"}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	const nodes = 16
	var wg sync.WaitGroup
	errs := make(chan error, nodes*8)
	for i := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("node-%02d", i)
			record := func(err error) {
				if err != nil {
					errs <- fmt.Errorf("%s: %w", id, err)
				}
			}
			record(c.CreateNode(ctx, store.Node{RunID: runID, NodeID: id, Status: "pending"}))
			record(c.StartNode(ctx, runID, id))
			record(c.StartNodeStep(ctx, runID, id, "run"))
			record(c.AppendEvent(ctx, runID, id, "node_started", []byte(`{}`)))
			record(c.AddNodeMetricSample(ctx, runID, id, store.MetricSample{
				TS: time.Now().UTC(), CPUMillicores: 100, MemoryBytes: 1 << 20,
			}))
			record(c.TouchNodeHeartbeat(ctx, runID, id))
			record(c.FinishNodeStep(ctx, runID, id, "run", store.StepPassed))
			record(c.FinishNode(ctx, runID, id, "success", "",
				[]byte(fmt.Sprintf(`{"n":%d}`, i))))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent node write: %v", err)
	}

	for i := range nodes {
		id := fmt.Sprintf("node-%02d", i)
		n, err := c.GetNode(ctx, runID, id)
		if err != nil {
			t.Fatalf("GetNode %s: %v", id, err)
		}
		if n.Outcome != "success" {
			t.Errorf("node %s outcome = %q, want success", id, n.Outcome)
		}
		out, err := c.GetNodeOutput(ctx, runID, id)
		if err != nil {
			t.Fatalf("GetNodeOutput %s: %v", id, err)
		}
		if want := fmt.Sprintf(`{"n":%d}`, i); string(out) != want {
			t.Errorf("node %s output = %s, want %s", id, out, want)
		}
	}

	if err := backend.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	rc, err := art.Get(ctx, "runs/"+runID+"/state.ndjson")
	if err != nil {
		t.Fatalf("read state blob: %v", err)
	}
	defer func() { _ = rc.Close() }()
	raw, _ := io.ReadAll(rc)
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var env struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal([]byte(line), &env) != nil {
			continue
		}

		var row struct {
			NodeID  string `json:"id"`
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal(env.Data, &row) == nil && row.Outcome == "success" {
			seen[row.NodeID] = true
		}
	}
	if len(seen) != nodes {
		t.Errorf("durable state carries %d finished nodes, want %d", len(seen), nodes)
	}
}
