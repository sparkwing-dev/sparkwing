package orchestrator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type downstreamCoordinatingRunner struct{ calls atomic.Int32 }

func (r *downstreamCoordinatingRunner) RunNode(context.Context, runner.Request) runner.Result {
	r.calls.Add(1)
	return runner.Result{Outcome: sparkwing.Success}
}

func (*downstreamCoordinatingRunner) CoordinatesDownstream() {}

func TestNewCoordinatingRunnerPreservesDownstreamCoordinator(t *testing.T) {
	downstream := &downstreamCoordinatingRunner{}
	if got := newCoordinatingRunner(Backends{}, downstream); got != downstream {
		t.Fatal("runner that coordinates in its execution process was wrapped by the upstream coordinator")
	}
	inProcess := NewInProcessRunner(Backends{})
	if got := newCoordinatingRunner(Backends{}, inProcess); got != inProcess {
		t.Fatal("explicit in-process runner was wrapped by a second in-process coordinator")
	}
}

func TestForceReleaseDefaultsToExistingInProcessBehavior(t *testing.T) {
	if !forceReleaseAllowed(context.Background()) {
		t.Fatal("ordinary in-process runner unexpectedly disabled timeout force-release")
	}
}

var errDownstreamRunner = errors.New("downstream runner failed")

type labeledFailingRunner struct {
	labels              []string
	calls               atomic.Int32
	forceReleaseAllowed atomic.Bool
}

func (r *labeledFailingRunner) AdvertisedLabels() []string { return r.labels }

func (r *labeledFailingRunner) RunNode(ctx context.Context, _ runner.Request) runner.Result {
	r.calls.Add(1)
	r.forceReleaseAllowed.Store(forceReleaseAllowed(ctx))
	return runner.Result{Outcome: sparkwing.Failed, Err: errDownstreamRunner}
}

func TestCoordinatingRunnerPreservesLabelsAndDownstreamResult(t *testing.T) {
	downstream := &labeledFailingRunner{labels: []string{"trusted", "arm64"}}
	wrapped := newCoordinatingRunner(Backends{}, downstream)
	advertiser, ok := wrapped.(runner.LabelAdvertiser)
	if !ok {
		t.Fatal("coordinating runner dropped LabelAdvertiser")
	}
	if got := advertiser.AdvertisedLabels(); len(got) != 2 || got[0] != "trusted" || got[1] != "arm64" {
		t.Fatalf("advertised labels = %v, want [trusted arm64]", got)
	}

	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "protected", func(context.Context) error { return nil })
	result := wrapped.RunNode(context.Background(), runner.Request{
		RunID:  "run",
		NodeID: node.ID(),
		Node:   node,
	})
	if result.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %q, want %q", result.Outcome, sparkwing.Failed)
	}
	if !errors.Is(result.Err, errDownstreamRunner) {
		t.Fatalf("error = %v, want downstream sentinel", result.Err)
	}
	if calls := downstream.calls.Load(); calls != 1 {
		t.Fatalf("downstream calls = %d, want 1", calls)
	}
	if downstream.forceReleaseAllowed.Load() {
		t.Fatal("wrapped custom runner allowed timeout force-release before cleanup acknowledgement")
	}
}

type groupedFailurePipeline struct{ sparkwing.Base }

func (groupedFailurePipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	group := sparkwing.NewConcurrencyGroup("grouped-failure", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		Scope:    sparkwing.ScopeBox,
		OnLimit:  sparkwing.Queue,
	})
	sparkwing.Job(plan, "protected", func(context.Context) error { return nil }).Concurrency(group)
	return nil
}

func TestCoordinatingRunnerPreservesGroupedDownstreamFailure(t *testing.T) {
	const pipeline = "coordinating-runner-grouped-failure"
	sparkwing.Register[sparkwing.NoInputs](pipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return groupedFailurePipeline{} })
	downstream := &labeledFailingRunner{}
	paths := PathsAt(t.TempDir())
	result, err := RunLocal(context.Background(), paths, Options{
		Pipeline: pipeline,
		Runner:   downstream,
	})
	if err != nil {
		t.Fatalf("RunLocal setup: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if result.Error == nil {
		t.Fatal("run error is nil, want grouped failure")
	}
	if calls := downstream.calls.Load(); calls != 1 {
		t.Fatalf("downstream calls = %d, want 1", calls)
	}
	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer func() { _ = state.Close() }()
	nodes, err := state.ListNodes(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != "protected" || nodes[0].Error != errDownstreamRunner.Error() {
		t.Fatalf("persisted nodes = %+v, want protected sentinel failure", nodes)
	}
}

type cancelOthersPipeline struct{ sparkwing.Base }

func (cancelOthersPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	group := sparkwing.NewConcurrencyGroup("custom-runner-cancel", sparkwing.ConcurrencyLimit{
		Capacity:      1,
		Scope:         sparkwing.ScopeRun,
		OnLimit:       sparkwing.CancelOthers,
		CancelTimeout: 20 * time.Millisecond,
	})
	for _, id := range []string{"first", "second"} {
		sparkwing.Job(plan, id, func(context.Context) error { return nil }).Concurrency(group)
	}
	return nil
}

type cleanupHoldingRunner struct {
	entered chan string
	cleanup chan struct{}
	calls   atomic.Int32
}

type forceReleaseSpy struct {
	ConcurrencyBackend
	cancelling     chan struct{}
	forced         chan struct{}
	cancellingOnce sync.Once
	forcedOnce     sync.Once
}

func (s *forceReleaseSpy) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	response, err := s.ConcurrencyBackend.AcquireSlot(ctx, req)
	if err == nil && response.Kind == store.AcquireCancellingOthers {
		s.cancellingOnce.Do(func() { close(s.cancelling) })
	}
	return response, err
}

func (s *forceReleaseSpy) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	s.forcedOnce.Do(func() { close(s.forced) })
	return s.ConcurrencyBackend.ForceReleaseSuperseded(ctx, key)
}

func (r *cleanupHoldingRunner) RunNode(_ context.Context, req runner.Request) runner.Result {
	call := r.calls.Add(1)
	r.entered <- req.NodeID
	if call == 1 {
		<-r.cleanup
	}
	return runner.Result{Outcome: sparkwing.Success}
}

func TestCustomRunnerCancelTimeoutWaitsForCleanupAcknowledgement(t *testing.T) {
	const pipeline = "custom-runner-cancel-cleanup"
	sparkwing.Register[sparkwing.NoInputs](pipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return cancelOthersPipeline{} })
	downstream := &cleanupHoldingRunner{
		entered: make(chan string, 2),
		cleanup: make(chan struct{}),
	}
	var cleanupOnce sync.Once
	releaseCleanup := func() { cleanupOnce.Do(func() { close(downstream.cleanup) }) }
	t.Cleanup(releaseCleanup)
	paths := PathsAt(t.TempDir())
	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer func() { _ = state.Close() }()
	backends := LocalBackends(paths, state, nil)
	spy := &forceReleaseSpy{
		ConcurrencyBackend: backends.Concurrency,
		cancelling:         make(chan struct{}),
		forced:             make(chan struct{}),
	}
	backends.Concurrency = spy
	done := make(chan *Result, 1)
	go func() {
		result, runErr := Run(context.Background(), backends, Options{
			Pipeline:    pipeline,
			Runner:      downstream,
			MaxParallel: 2,
		})
		if runErr != nil {
			done <- &Result{Status: "setup-failed", Error: runErr}
			return
		}
		done <- result
	}()
	select {
	case <-downstream.entered:
	case <-time.After(5 * time.Second):
		releaseCleanup()
		t.Fatal("initial custom-runner holder did not enter")
	}
	select {
	case <-spy.cancelling:
	case <-time.After(5 * time.Second):
		releaseCleanup()
		t.Fatal("replacement did not reach CancelOthers admission")
	}
	forcedEarly := false
	select {
	case <-spy.forced:
		forcedEarly = true
	case <-time.After(350 * time.Millisecond):
	}
	replacementEnteredEarly := false
	select {
	case <-downstream.entered:
		replacementEnteredEarly = true
	default:
	}
	releaseCleanup()
	if !replacementEnteredEarly {
		select {
		case <-downstream.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("replacement did not enter after holder cleanup acknowledgement")
		}
	}
	select {
	case result := <-done:
		if result.Status != "cancelled" {
			t.Fatalf("run status = %q, error = %v; want cancelled after supersede", result.Status, result.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel-cleanup run did not finish")
	}
	if forcedEarly || replacementEnteredEarly {
		t.Fatalf("before cleanup acknowledgement: forced=%v replacement_entered=%v", forcedEarly, replacementEnteredEarly)
	}
}

var errRejectedRunnerPlan = errors.New("runner rejected plan")

type rejectingPlanRunner struct{ calls atomic.Int32 }

func (*rejectingPlanRunner) ValidatePlan(context.Context, runner.PlanValidationRequest) error {
	return errRejectedRunnerPlan
}

func (r *rejectingPlanRunner) RunNode(context.Context, runner.Request) runner.Result {
	r.calls.Add(1)
	return runner.Result{Outcome: sparkwing.Success}
}

type guardedPlanPipeline struct{ sparkwing.Base }

func (guardedPlanPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "protected", func(context.Context) error { return nil }).
		Inline().
		Cache(func(context.Context) sparkwing.CacheKey { return "candidate-key" })
	return nil
}

func TestRunValidatesRunnerPlanBeforeInlineOrCacheDispatch(t *testing.T) {
	const pipeline = "runner-plan-guard"
	sparkwing.Register[sparkwing.NoInputs](pipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return guardedPlanPipeline{}
	})
	downstream := &rejectingPlanRunner{}
	paths := PathsAt(t.TempDir())
	result, err := RunLocal(context.Background(), paths, Options{
		Pipeline: pipeline,
		Runner:   downstream,
	})
	if err != nil {
		t.Fatalf("RunLocal setup: %v", err)
	}
	if !errors.Is(result.Error, errRejectedRunnerPlan) {
		t.Fatalf("run error = %v, want runner plan rejection", result.Error)
	}
	if calls := downstream.calls.Load(); calls != 0 {
		t.Fatalf("downstream runner calls = %d, want zero after plan rejection", calls)
	}
	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer func() { _ = state.Close() }()
	run, err := state.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("get rejected run: %v", err)
	}
	if len(run.PlanSnapshot) != 0 {
		t.Fatalf("rejected plan persisted %d projection bytes", len(run.PlanSnapshot))
	}
	nodes, err := state.ListNodes(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("list rejected nodes: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("rejected plan created %d nodes", len(nodes))
	}
}

type capturingValidationRunner struct {
	validation          runner.PlanValidationRequest
	node                runner.Request
	validatedAcceptance string
}

func (r *capturingValidationRunner) ValidatePlan(_ context.Context, req runner.PlanValidationRequest) error {
	r.validation = req
	r.validatedAcceptance = req.Args["acceptance"]
	req.Args["acceptance"] = "mutated-by-validator"
	if req.RunContext.Trigger.PullRequest != nil {
		req.RunContext.Trigger.PullRequest.HeadSHA = "mutated-by-validator"
	}
	return nil
}

func (r *capturingValidationRunner) RunNode(_ context.Context, req runner.Request) runner.Result {
	r.node = req
	return runner.Result{Outcome: sparkwing.Success}
}

type validationIdentityPipeline struct{ sparkwing.Base }

func (validationIdentityPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "protected", func(context.Context) error { return nil })
	return nil
}

func TestPlanValidationAndNodeDispatchShareAuthoritativeIdentity(t *testing.T) {
	const pipeline = "runner-plan-identity"
	sparkwing.Register[sparkwing.NoInputs](pipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return validationIdentityPipeline{} })
	gitIdentity := sparkwing.NewGit(t.TempDir(), "0123456789abcdef0123456789abcdef01234567", "main", "main", "sparkwing-dev/sparkwing", "https://github.com/sparkwing-dev/sparkwing.git")
	downstream := &capturingValidationRunner{}
	result, err := RunLocal(context.Background(), PathsAt(t.TempDir()), Options{
		Pipeline: pipeline,
		RunID:    "authoritative-run",
		Args:     map[string]string{"acceptance": "phase-b"},
		Git:      gitIdentity,
		Trigger: sparkwing.TriggerInfo{Source: "push", User: "operator", PullRequest: &sparkwing.PullRequest{
			Number: 17, BaseRef: "main", BaseSHA: "base", HeadRef: "candidate", HeadSHA: gitIdentity.SHA,
		}},
		Profile: &profile.Profile{Name: "trusted-phaseb"},
		Runner:  downstream,
	})
	if err != nil || result.Status != "success" {
		t.Fatalf("RunLocal status = %q, error = %v / %v", result.Status, err, result.Error)
	}
	validation := downstream.validation
	if validation.RunContext.RunID != "authoritative-run" || validation.RunContext.Pipeline != pipeline {
		t.Fatalf("validation run identity = %+v", validation.RunContext)
	}
	if validation.RunContext.Git == nil || validation.RunContext.Git.SHA != gitIdentity.SHA || validation.RunContext.Trigger.Source != "push" {
		t.Fatalf("validation source identity = git %+v, trigger %+v", validation.RunContext.Git, validation.RunContext.Trigger)
	}
	if validation.ProfileName != "trusted-phaseb" || !validation.ProfileIsLocal {
		t.Fatalf("validation profile = %q local=%v", validation.ProfileName, validation.ProfileIsLocal)
	}
	if downstream.validatedAcceptance != "phase-b" {
		t.Fatalf("validated acceptance arg = %q", downstream.validatedAcceptance)
	}
	if validation.ProjectionDigest == "" || validation.ProjectionDigest != hashBytes(validation.Projection) {
		t.Fatalf("validation projection digest = %q for %d bytes", validation.ProjectionDigest, len(validation.Projection))
	}
	if downstream.node.RunID != validation.RunContext.RunID || downstream.node.Pipeline != validation.RunContext.Pipeline || downstream.node.PlanDigest != validation.ProjectionDigest {
		t.Fatalf("node identity = %+v; validation = %+v", downstream.node, validation)
	}
	if downstream.node.Git == nil || downstream.node.Git.SHA != gitIdentity.SHA || downstream.node.Trigger.Source != "push" || downstream.node.ProfileName != "trusted-phaseb" || !downstream.node.ProfileIsLocal {
		t.Fatalf("node source/profile identity = %+v", downstream.node)
	}
	if downstream.node.Trigger.PullRequest == nil || downstream.node.Trigger.PullRequest.HeadSHA != gitIdentity.SHA {
		t.Fatalf("node pull-request identity = %+v, want immutable head %s", downstream.node.Trigger.PullRequest, gitIdentity.SHA)
	}
	if downstream.node.Args["acceptance"] != "phase-b" {
		t.Fatalf("node args = %v", downstream.node.Args)
	}
}
