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

	"github.com/sparkwing-dev/sparkwing/internal/fleet"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
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
	}).Requires("helper-cap")
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

// safety: this schema-30 causal covers foreground authority admission and award
// only. Helper body execution awaits schema 31's fenced body route and grants.
func TestForegroundFleetAuthorityAdmitsAndAwardsHelperThenFallsBackToCoordinator(t *testing.T) {
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
	raw, token, err := st.ProvisionExecutor(ctx, "fleet-executor:helper", store.Executor{
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
			FleetSourceBundle: fixture.bundle, FleetSourceSHA: fixture.sha,
			FleetSourceRepoURL: fixture.repoURL, MaxParallel: 2,
		})
		done <- runResult{result: result, err: runErr}
	}()

	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	remote := client.NewWithToken("http://"+address, &http.Client{Transport: transport, Timeout: time.Second}, raw)
	deadline := time.Now().Add(8 * time.Second)
	for {
		err = remote.HeartbeatExecutor(ctx, "helper", client.Headroom{Cores: 4, MemoryBytes: 8 << 30})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("authenticated Fleet listener did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	var preparation *store.ExecutorClaimPreparation
	for preparation == nil {
		preparation, err = remote.PrepareExecutorClaim(ctx, "helper")
		if err != nil {
			t.Fatalf("prepare helper claim over Fleet listener: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never observed the ready Fleet node")
		}
		if preparation == nil {
			time.Sleep(20 * time.Millisecond)
		}
	}

	offer := client.ExecutorClaim{
		ExecutorName: "helper", HolderID: "executor:helper:test:0",
		ReservationID: "reservation-test", ResourceDigest: preparation.Summary.ResourceDigest,
		Slot: 0, Lease: time.Minute,
	}
	var awarded *store.Node
	for awarded == nil {
		result, offerErr := remote.OfferExecutorClaim(ctx, offer, preparation.Summary.RunID, preparation.Summary.NodeID)
		if offerErr != nil {
			t.Fatalf("offer helper claim over Fleet listener: %v", offerErr)
		}
		awarded = result.Node
		if awarded == nil {
			stored, getErr := st.GetNode(ctx, runID, "helper")
			if getErr == nil && stored.Claimed {
				awarded = stored
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("Fleet helper offer was not awarded")
		}
		if awarded == nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if awarded.NodeID != "helper" || awarded.ExecutorName != "helper" || awarded.ExecutorLocation != "local" {
		t.Fatalf("awarded helper attribution = node %q executor %q location %q", awarded.NodeID, awarded.ExecutorName, awarded.ExecutorLocation)
	}

	claimant := store.ClaimIdentity{Principal: token.Principal, TokenPrefix: token.Prefix}
	fence := store.NodeClaimFence{
		Claimant: claimant, HolderID: awarded.ClaimedBy, MembershipID: awarded.ClaimMembershipID,
		ReservationID: awarded.ReservationID, ClaimGeneration: awarded.ClaimGeneration,
	}
	fenced := store.WithNodeClaimFence(ctx, fence)
	start := store.ExecutionStart{
		HolderID: fence.HolderID, MembershipID: fence.MembershipID,
		ReservationID: fence.ReservationID, ClaimGeneration: fence.ClaimGeneration, AttemptOrdinal: 1,
	}
	if err := st.AcknowledgeNodeExecutionStart(fenced, runID, "helper", claimant, start); err != nil {
		t.Fatalf("acknowledge helper execution boundary: %v", err)
	}
	if err := st.FinishNodeExecutionAttempt(fenced, runID, "helper", claimant, store.ExecutionAttemptFinish{
		HolderID: fence.HolderID, MembershipID: fence.MembershipID,
		ReservationID: fence.ReservationID, ClaimGeneration: fence.ClaimGeneration,
		AttemptOrdinal: 1, Outcome: string(sparkwing.Success),
	}); err != nil {
		t.Fatalf("finish helper execution boundary: %v", err)
	}
	if err := st.FinishNode(fenced, runID, "helper", string(sparkwing.Success), "", nil); err != nil {
		t.Fatalf("settle awarded helper node: %v", err)
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
	if got := fleetHelperBodyCalls.Load(); got != 0 {
		t.Fatalf("schema-30 helper body calls = %d, want 0", got)
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
