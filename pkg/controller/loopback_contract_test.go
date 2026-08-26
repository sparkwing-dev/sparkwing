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

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// memArt is an in-memory ArtifactStore, the same shape the s3state
// package's own tests use, so the loopback runs over the object-store
// state backend without an AWS endpoint.
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

// s3Adapter is the orchestrator's own object-store StateBackend shape:
// *s3state.Backend answers every method of it directly. Declared here
// so the contract test drives the exact surface the orchestrator hands
// the loopback, without importing the orchestrator (which imports this
// package).
type s3Adapter struct{ *s3state.Backend }

// loopbackToken is the run-scoped bearer both sides of the contract
// authenticate with.
const loopbackToken = "swl_contract"

// contractRunID is the run every loopback in this file is scoped to.
const contractRunID = "run-contract"

// TestLoopbackContract_EveryRouteTheNodeClientCalls drives the surface
// a coordinated node process reaches through client.Client against the
// loopback over object-store state.
//
// The list is the scouted call set, not a sample: RunNodeOnce's own
// reads, everything it installs on the context (ref resolution, the
// cross-pipeline resolver, the awaiter), the state writes the execution
// path makes for every node, the spawn handler's child rows, the
// metrics and heartbeat wires, and the artifact manifest. A route that
// leaves this list is a route a node can no longer reach.
//
// Every route is EXERCISED on both halves, but not every route is
// ASSERTED on both. The object-store backend refuses the records that
// need compare-and-swap when the store cannot do it, and an in-memory
// bucket cannot: triggers (enqueue, spawned-child lookup), approvals,
// debug pauses, and dispatch snapshots therefore reach a real
// assertion only against SQLite here, and answer not-supported on the
// s3 half. That refusal is itself part of the contract -- it has to
// arrive as a declined operation rather than a transport failure --
// which is what isUnsupported checks. The cross-runner CAS behavior of
// those same records is covered in pkg/storage/s3state.
func TestLoopbackContract_EveryRouteTheNodeClientCalls(t *testing.T) {
	t.Parallel()
	art := newMemArt()
	backend := s3state.New(art, s3state.WithFlushInterval(10*time.Millisecond))
	t.Cleanup(func() { _ = backend.Close() })

	c, _ := newLoopbackClient(t, s3Adapter{Backend: backend}, contractRunID, nil, art)
	runContractSurface(t, c, "s3")

	// safety: the run really landed on the bucket, not only in the backend's
	// memory -- Mode 2's whole promise is the NDJSON object.
	if err := backend.Close(); err != nil {
		t.Fatalf("close backend: %v", err)
	}
	if has, _ := art.Has(context.Background(), "runs/run-contract/state.ndjson"); !has {
		t.Error("no state.ndjson landed in the bucket for run-contract")
	}
}

// TestLoopbackContract_MatchesTheRealController runs the identical
// surface against controller.Server over a SQLite store. It is the
// anti-drift half: the loopback's route patterns, status codes, and
// wire types are only "the controller's" for as long as the same client
// calls succeed against both.
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

// newLoopbackClient mounts a loopback scoped to runID and returns a
// client carrying the run's bearer.
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

// runContractSurface exercises every controller call a node process
// makes. backing names which state is underneath, so a failure says
// which of the two answered wrong.
func runContractSurface(t *testing.T, c *client.Client, backing string) {
	t.Helper()
	ctx := context.Background()
	const runID = contractRunID

	// --- what the dispatcher wrote before the child started ---
	if err := c.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "contract", Status: "running",
		Args: map[string]string{"token": "s3cret"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := c.UpdatePlanSnapshot(ctx, runID, []byte(`{"nodes":[]}`)); err != nil {
		t.Fatalf("UpdatePlanSnapshot: %v", err)
	}

	// --- RunNodeOnce's opening reads ---
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

	// --- the node's own row, start to terminal ---
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

	// --- per-step state ---
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

	// --- terminal row and the typed output crossing back ---
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

	// --- the spawn handler's child row, with lineage in its id ---
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

	// --- the dispatch snapshot ---
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

	// --- cross-pipeline ref resolution ---
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
	// --- the awaiter's child lookup and spawn ---
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
		// The spawned child is findable by (parent run, parent node,
		// pipeline) afterward, which is what threads retry lineage across
		// a re-run of the parent.
		//
		// Repeat-enqueue behavior is NOT asserted, because the two backings
		// genuinely differ and neither is this shim's doing: the object
		// store makes the child index a PutIfAbsent and returns the
		// original id, while the controller mints a fresh trigger every
		// call. Nothing on the node path enqueues twice for one node
		// execution, so the divergence is latent rather than live.
		if id, ferr := c.FindSpawnedChildTriggerID(ctx, runID, "produce", "child-pipeline"); ferr == nil && id == "" {
			t.Errorf("FindSpawnedChildTriggerID found no child after enqueueing %q", childRunID)
		}
	}

	// --- debug pauses ---
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

	// --- approvals ---
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

	// --- not-found is the same error on both backings ---
	if _, err := c.GetRun(ctx, "run-does-not-exist"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetRun(missing) on %s = %v, want store.ErrNotFound", backing, err)
	}
	if _, err := c.GetNode(ctx, runID, "no-such-node"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetNode(missing) on %s = %v, want store.ErrNotFound", backing, err)
	}
}

// isUnsupported reports whether err is a backend declining an
// operation it does not implement, which is the escape hatch the
// assertions above take on the s3 half.
//
// It is reached for exactly the CAS-dependent records: trigger enqueue
// and spawned-child lookup, approvals, debug pauses, and dispatch
// snapshots. An in-memory bucket implements no conditional writes, so
// the object-store backend declines those and the assertions that
// follow them run only against SQLite. Everything else -- run and node
// rows, outputs, steps, events, metrics, heartbeats -- is asserted on
// both halves.
//
// The string check is not laziness: the client turns a 501 into a
// transport-shaped error and the sentinel does not survive the wire, so
// the message is what is left to key on.
func isUnsupported(err error) bool {
	return errors.Is(err, storage.ErrNotSupported) ||
		strings.Contains(err.Error(), "not supported")
}

// TestLoopback_RefusesMutationsAimedAtAnotherRun is the blast-radius
// gate on the run-scoped bearer.
//
// The token sits in the environment of every node process the run
// spawned, and a node body is arbitrary user code. On a shared CI
// bucket every other run in the organization is in the same backing
// store, so an ungated loopback would let one run's node finish,
// cancel, or rewrite another's rows. Reads stay open: a cross-pipeline
// ref and a RunAndAwait poll read runs that are legitimately not this
// one.
func TestLoopback_RefusesMutationsAimedAtAnotherRun(t *testing.T) {
	t.Parallel()
	art := newMemArt()
	// safety: a one-byte buffer threshold flushes every append, so the
	// cross-pipeline read below sees the seeded run in the bucket's key
	// listing rather than racing the flush ticker.
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

	// safety: the victim's rows are untouched -- a refusal that still wrote
	// would pass the loop above.
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

	// safety: reads across runs must still work, or a cross-pipeline ref and
	// every RunAndAwait poll break.
	if got, rerr := c.GetRun(ctx, victim); rerr != nil || got.Pipeline != "other" {
		t.Errorf("cross-run read = (%+v, %v), want the victim run", got, rerr)
	}
	if _, rerr := c.GetNode(ctx, victim, "n"); rerr != nil {
		t.Errorf("cross-run node read: %v", rerr)
	}
	if _, rerr := c.GetLatestRun(ctx, "other", []string{"running"}, time.Hour); rerr != nil {
		t.Errorf("cross-pipeline latest read: %v", rerr)
	}

	// safety: the same calls against the loopback's own run still work, so
	// the gate is scoping and not blanket refusal.
	if err := c.CreateNode(ctx, store.Node{RunID: contractRunID, NodeID: "mine", Status: "pending"}); err != nil {
		t.Fatalf("same-run CreateNode: %v", err)
	}
	if err := c.FinishNode(ctx, contractRunID, "mine", "success", "", []byte(`{}`)); err != nil {
		t.Fatalf("same-run FinishNode: %v", err)
	}
}

// brokenState fails one read the way a transport fault would: not
// found, not unsupported, just broken.
type brokenState struct{ controller.LoopbackState }

func (brokenState) GetRun(context.Context, string) (*store.Run, error) {
	return nil, errors.New("bucket unreachable")
}

// TestLoopback_PlainBackendErrorIs500 pins the fallthrough of the
// error mapping. A state error that is neither absence nor a declined
// operation has to reach the client as a 500 carrying its message --
// the arm with no sentinel to key on, and therefore the one a mapping
// bug hides in.
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
	if !strings.Contains(string(body), "bucket unreachable") {
		t.Errorf("body = %s, want the backend's message", body)
	}
}

// TestLoopback_RejectsAnyOtherBearer pins the run-scoped credential.
// The listener is on loopback, but every process on the box can reach
// it, and the token is the only thing separating this run's state from
// them.
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

	// safety: health has to stay open, for the same reason it is open on the
	// real controller -- a probe must not need the run's credential.
	resp, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want 200", resp.StatusCode)
	}
}

// TestLoopback_ConcurrentNodeWritesSerializeOnOneRun is the
// process-per-node concurrency claim: N node processes write one run's
// state through one shim, so the object-store backend -- which
// rewrites the run's whole NDJSON blob on every flush -- sees one
// writer, not N.
//
// Run with -race, the assertion is that no write is lost: every node's
// terminal row is present afterward.
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

	// safety: the durable blob has to carry every node too, not just the
	// backend's memory -- a lost envelope is only visible after a flush.
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
		// safety: a node row's id field is "id", not "node_id"; matching the
		// wrong name would count nothing and pass vacuously.
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
