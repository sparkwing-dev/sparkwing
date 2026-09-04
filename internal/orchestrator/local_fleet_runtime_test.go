package orchestrator

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/internal/fleet"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/runners/warmpool"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const fleetAuthorityWiringPipeline = "fleet-authority-admission-award-e2e"

var (
	fleetHelperBodyCalls      atomic.Int64
	fleetCoordinatorBodyCalls atomic.Int64
)

type fleetAuthorityWiringPipe struct{ sparkwing.Base }

func (fleetAuthorityWiringPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	helper := sparkwing.Job(plan, "helper", func(context.Context) error {
		fleetHelperBodyCalls.Add(1)
		return nil
	})
	sparkwing.Job(plan, "coordinator", func(context.Context) error {
		fleetCoordinatorBodyCalls.Add(1)
		return nil
	}).Requires("location=coordinator").Needs(helper)
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs](fleetAuthorityWiringPipeline,
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return &fleetAuthorityWiringPipe{} })
}

func TestPrepareLocalFleetRuntimeRequiresExactConfigAndLocalSQLite(t *testing.T) {
	if _, err := prepareLocalFleetRuntime(Backends{}, Options{}); err == nil || err.Error() != "fleet coordinator config path is required" {
		t.Fatalf("empty config path error = %v", err)
	}
	address := reserveFleetTestAddress(t)
	configPath := filepath.Join(t.TempDir(), "config", fleet.Filename)
	if err := fleet.Create(configPath, fleet.Config{
		Listen: address, PublicURL: "http://" + address,
		Local: fleet.Local{Name: "local", MaxConcurrent: 1, Contribution: "50%,50%"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLocalFleetRuntime(Backends{}, Options{FleetConfigPath: configPath}); !errors.Is(err, errFleetLocalStoreRequired) {
		t.Fatalf("non-local backend error = %v", err)
	}
}

// safety: a schema-31 helper may observe a sealed candidate, but cannot reserve
// capacity or execute it until its exact compiled body has been attested.
func TestForegroundFleetAuthorityRequiresBodyAttestationThenFallsBackToCoordinator(t *testing.T) {
	fleetHelperBodyCalls.Store(0)
	fleetCoordinatorBodyCalls.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	root := t.TempDir()
	paths := PathsAt(filepath.Join(root, "sparkwing"))
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	runID := "fleet-authority-wiring"
	if err := paths.EnsureRunDir(runID); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw, _, err := st.ProvisionExecutor(ctx, "fleet-executor:helper", store.Executor{
		Name: "helper", Kind: "agent", Location: "local", Capabilities: []string{"helper-cap"},
		BasePriority: 100, PriorityCeiling: 100, MaxConcurrent: 1,
		Budget: store.ExecutorResource{Cores: 4, MemoryBytes: 8 << 30},
	}, []string{controller.ScopeNodesClaim, controller.ScopeRunsState}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	address := reserveFleetTestAddress(t)
	configPath := filepath.Join(root, "config", fleet.Filename)
	if err := fleet.Create(configPath, fleet.Config{
		Listen: address, PublicURL: "http://" + address,
		Local: fleet.Local{
			Name: "coordinator", Capabilities: []string{"coordinator-cap"},
			MaxConcurrent: 1, Contribution: "50%,50%",
		},
		Executors: []fleet.Executor{{
			Name: "helper", Location: "local", Capabilities: []string{"helper-cap"},
			BasePriority: 100, PriorityCeiling: 100, MaxConcurrent: 1,
			Budget: store.ExecutorResource{Cores: 4, MemoryBytes: 8 << 30},
		}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	fixture := newFleetSourceFixture(t)

	type runResult struct {
		result *Result
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, runErr := Run(ctx, LocalBackends(paths, st, nil), Options{
			Pipeline: fleetAuthorityWiringPipeline, RunID: runID, Fleet: true,
			FleetConfigPath: configPath, FleetSourceRoot: fixture.root,
			FleetSourceBundle: fixture.bundle, FleetSourceSHA: fixture.sha, FleetSourceManifestDigest: fixture.manifest,
			FleetSourceRepoURL: fixture.repoURL, MaxParallel: 2,
		})
		done <- runResult{result: result, err: runErr}
	}()

	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	remote := client.NewWithToken("http://"+address, &http.Client{Transport: transport, Timeout: time.Second}, raw)
	deadline := time.Now().Add(8 * time.Second)
	runtimeCtx, err := executionpolicy.WithRuntimeReport(ctx, executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v0.41.0", GOOS: "linux", GOARCH: "amd64",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for {
		err = remote.HeartbeatExecutor(runtimeCtx, "helper", client.Headroom{Cores: 4, MemoryBytes: 8 << 30})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("authenticated Fleet listener did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	sink := executionpolicy.NewPreparationSink()
	prepareCtx := executionpolicy.WithPreparationSink(runtimeCtx, sink)
	for {
		_, err = remote.PrepareExecutorClaim(prepareCtx, "helper")
		if errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
			break
		}
		if err != nil {
			select {
			case completed := <-done:
				t.Fatalf("foreground run stopped before assisted refusal: result %+v, err %v; prepare: %v", completed.result, completed.err, err)
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("prepare helper claim over Fleet listener: %v", err)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never observed the sealed Fleet node")
		}
		time.Sleep(20 * time.Millisecond)
	}
	binding := sink.Load()
	if binding.RunID != runID || binding.NodeID != "helper" {
		t.Fatalf("private body-attestation binding = %+v", binding)
	}
	var offers int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM node_claim_offers WHERE run_id = ?`, runID).Scan(&offers); err != nil {
		t.Fatal(err)
	}
	if offers != 0 {
		t.Fatalf("body-attestation refusal created %d capacity offers", offers)
	}
	candidate, err := st.GetNode(ctx, runID, "helper")
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Claimed || candidate.ClaimedBy != "" || candidate.ClaimGeneration != 0 || fleetHelperBodyCalls.Load() != 0 {
		t.Fatalf("helper crossed the refusal boundary: node=%+v body_calls=%d", candidate, fleetHelperBodyCalls.Load())
	}

	var completed runResult
	select {
	case completed = <-done:
	case <-ctx.Done():
		t.Fatal("foreground Fleet run did not finish")
	}
	if completed.err != nil || completed.result == nil || completed.result.Error != nil || completed.result.Status != "success" {
		t.Fatalf("foreground Fleet run = result %+v, err %v", completed.result, completed.err)
	}
	if got := fleetHelperBodyCalls.Load(); got != 1 {
		t.Fatalf("coordinator fallback candidate body calls = %d, want 1", got)
	}
	if got := fleetCoordinatorBodyCalls.Load(); got != 1 {
		t.Fatalf("coordinator fallback body calls = %d, want 1", got)
	}
	coordinatorNode, err := st.GetNode(ctx, runID, "coordinator")
	if err != nil {
		t.Fatal(err)
	}
	if coordinatorNode.Status != "done" || coordinatorNode.Outcome != string(sparkwing.Success) {
		t.Fatalf("coordinator fallback node = %+v", coordinatorNode)
	}
	if coordinatorNode.ClaimedBy != "" || coordinatorNode.ClaimGeneration != 0 || coordinatorNode.ExecutorName != "" {
		t.Fatalf("coordinator fallback acquired a second admission: %+v", coordinatorNode)
	}
	candidate, err = st.GetNode(ctx, runID, "helper")
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ExecutorName != "" || candidate.ExecutorLocation == "local" || candidate.ClaimGeneration != 0 {
		t.Fatalf("fallback retained helper attribution: %+v", candidate)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	if resp, requestErr := (&http.Client{Transport: transport, Timeout: 300 * time.Millisecond}).Do(req); requestErr == nil {
		_ = resp.Body.Close()
		t.Fatal("Fleet listener remained reachable after FinishRun and reporting")
	}
}

type fleetFallbackCounter struct{ calls atomic.Int64 }

func (r *fleetFallbackCounter) RunNode(context.Context, runner.Request) runner.Result {
	r.calls.Add(1)
	return runner.Result{Outcome: sparkwing.Success}
}

func TestForegroundFleetHelperOnlyNodeStaysSealedPendingWithoutCoordinatorFallback(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	gitSHA := strings.Repeat("a", 40)
	manifest := "sha256:" + strings.Repeat("b", 64)
	if err := st.CreateRun(ctx, store.Run{
		ID: "helper-only", Pipeline: "release", Status: "running", GitSHA: gitSHA, StartedAt: time.Now(),
		Invocation: map[string]any{"fleet_source": map[string]any{
			"kind": "working_tree", "identity": gitSHA, "manifest_digest": manifest,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	plan := []byte(`{"pipeline":"release","run_id":"helper-only","nodes":[{"id":"work","deps":[],"work":{"steps":[{"id":"run"}]}}]}`)
	if err := st.UpdatePlanSnapshot(ctx, "helper-only", plan); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "helper-only", NodeID: "work", Status: "pending", NeedsLabels: []string{"helper-cap"},
	}); err != nil {
		t.Fatal(err)
	}
	fallback := &fleetFallbackCounter{}
	warm := warmpool.New(localStoreFleetCoordinator{store: st}, fallback, warmpool.Config{
		PollInterval: time.Millisecond, ClaimWaitTimeout: 10 * time.Millisecond,
		FallbackLabels: []string{"local", "location=coordinator"},
	}, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan runner.Result, 1)
	go func() {
		done <- warm.RunNode(runCtx, runner.Request{RunID: "helper-only", NodeID: "work"})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		node, getErr := st.GetNode(ctx, "helper-only", "work")
		var seals int
		if getErr == nil {
			if err := st.DB().QueryRow(`SELECT COUNT(*) FROM nodes WHERE run_id = 'helper-only' AND node_id = 'work' AND execution_policy_hash != ''`).Scan(&seals); err != nil {
				t.Fatal(err)
			}
		}
		if getErr == nil && node.ReadyAt != nil && !node.Claimed && seals == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper-only node did not become sealed and pending: node=%+v err=%v seals=%d", node, getErr, seals)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case result := <-done:
		t.Fatalf("helper-only node fell back to coordinator: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	if fallback.calls.Load() != 0 {
		t.Fatalf("helper-only coordinator fallback calls = %d, want 0", fallback.calls.Load())
	}
	cancel()
	select {
	case result := <-done:
		if result.Outcome != sparkwing.Cancelled {
			t.Fatalf("cancelled helper-only result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("helper-only runner did not drain after cancellation")
	}
}

func reserveFleetTestAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestFleetCoordinatorLabelsAreBoundedAndStable(t *testing.T) {
	got := fleetCoordinatorLabels([]string{"gpu", "gpu", "local"})
	if joined := strings.Join(got, ","); joined != "gpu,local,location=coordinator" {
		t.Fatalf("coordinator labels = %q", joined)
	}
}

type fleetConcurrencyProbeRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *fleetConcurrencyProbeRunner) RunNode(context.Context, runner.Request) runner.Result {
	r.started <- struct{}{}
	<-r.release
	return runner.Result{Outcome: sparkwing.Success}
}

func TestLimitedFleetRunnerEnforcesCoordinatorMaxConcurrent(t *testing.T) {
	probe := &fleetConcurrencyProbeRunner{started: make(chan struct{}, 2), release: make(chan struct{})}
	r := &limitedFleetRunner{runner: probe, slots: make(chan struct{}, 1)}
	done := make(chan runner.Result, 2)
	for range 2 {
		go func() { done <- r.RunNode(context.Background(), runner.Request{}) }()
	}
	select {
	case <-probe.started:
	case <-time.After(time.Second):
		t.Fatal("first coordinator fallback did not start")
	}
	select {
	case <-probe.started:
		t.Fatal("second coordinator fallback exceeded max_concurrent")
	case <-time.After(50 * time.Millisecond):
	}
	close(probe.release)
	for range 2 {
		select {
		case result := <-done:
			if result.Outcome != sparkwing.Success || result.Err != nil {
				t.Fatalf("fallback result = %+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("coordinator fallback did not finish")
		}
	}
}
