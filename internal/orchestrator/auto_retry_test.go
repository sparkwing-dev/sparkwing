package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/runners/warmpool"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var autoRetryCount atomic.Int32

type autoRetryFlakyJob struct {
	sparkwing.Base
	failUntilDispatch int32
}

func (j *autoRetryFlakyJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(ctx context.Context) error {
		dispatch := autoRetryCount.Add(1)
		if dispatch <= j.failUntilDispatch {
			return errors.New("infra flake (simulated)")
		}
		return nil
	})
	return nil, nil
}

type autoRetrySuccessAfterTwoFailsPipe struct{ sparkwing.Base }

func (autoRetrySuccessAfterTwoFailsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "flaky", &autoRetryFlakyJob{failUntilDispatch: 2}).
		Retry(2, sparkwing.RetryBackoff(10*time.Millisecond), sparkwing.RetryAuto())
	return nil
}

type autoRetryExhaustsAttemptsPipe struct{ sparkwing.Base }

func (autoRetryExhaustsAttemptsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "always-fails", &autoRetryFlakyJob{failUntilDispatch: 100}).
		Retry(2, sparkwing.RetryAuto())
	return nil
}

type autoRetryOneFailurePipe struct{ sparkwing.Base }

func (autoRetryOneFailurePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "mixed", func(context.Context) error {
		autoRetryCount.Add(1)
		return errors.New("fallback failure")
	}).Retry(1, sparkwing.RetryAuto())
	return nil
}

type autoRetryOneSuccessPipe struct{ sparkwing.Base }

func (autoRetryOneSuccessPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "mixed", func(context.Context) error {
		autoRetryCount.Add(1)
		return nil
	}).Retry(1, sparkwing.RetryAuto())
	return nil
}

func init() {
	register("auto-retry-recovers", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &autoRetrySuccessAfterTwoFailsPipe{} })
	register("auto-retry-exhausts", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &autoRetryExhaustsAttemptsPipe{} })
	register("auto-retry-one-failure", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &autoRetryOneFailurePipe{} })
	register("auto-retry-one-success", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &autoRetryOneSuccessPipe{} })
}

func claimedRetryContext(t *testing.T, st *store.Store, runID, pipeline string) context.Context {
	t.Helper()
	ctx := context.Background()
	identity := store.ClaimIdentity{Principal: "source-runner", TokenPrefix: "swr_source"}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: pipeline, Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := st.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{
		Claimant: identity, ClaimGeneration: trigger.ClaimSeq,
	})
}

func enrollMixedExecutor(t *testing.T, st *store.Store) store.ClaimIdentity {
	t.Helper()
	identity := store.ClaimIdentity{Principal: "mixed-agent", TokenPrefix: "swr_mixed_agent"}
	if err := st.EnrollExecutor(context.Background(), identity.TokenPrefix, store.Executor{
		Name: "mixed-agent", Kind: "agent", Location: "local", Principal: identity.Principal,
		BasePriority: 100, PriorityCeiling: 100, MaxConcurrent: 1,
		Budget: store.ExecutorResource{Cores: 4, MemoryBytes: 8 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.HeartbeatExecutor(context.Background(), identity, "mixed-agent",
		store.ExecutorResource{Cores: 4, MemoryBytes: 8 << 30}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	return identity
}

func awaitLegacyMixedClaim(t *testing.T, ctx context.Context, st *store.Store, runID string, identity store.ClaimIdentity) *store.Node {
	return awaitLegacyMixedClaimAfterAttempts(t, ctx, st, runID, 0, identity)
}

func awaitLegacyMixedClaimAfterAttempts(t *testing.T, ctx context.Context, st *store.Store, runID string, consumed int, identity store.ClaimIdentity) *store.Node {
	t.Helper()
	for {
		node, err := st.GetNode(ctx, runID, "mixed")
		if err == nil && node.ReadyAt != nil && node.AttemptsConsumed < consumed && !node.Claimed {
			_, _ = st.DB().ExecContext(ctx, `UPDATE nodes SET offer_started_at = ? WHERE run_id = ? AND node_id = ?`,
				time.Now().Add(-time.Minute).UnixNano(), runID, "mixed")
		}
		if err == nil && node.ReadyAt != nil && node.AttemptsConsumed == consumed && !node.Claimed {
			claimed, claimErr := st.ClaimNextReadyNode(store.WithoutClaimFences(ctx), identity, "legacy-holder", time.Minute, nil)
			if claimErr == nil {
				return claimed
			}
			if !errors.Is(claimErr, store.ErrNotFound) {
				t.Fatal(claimErr)
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

func forceMixedFallback(t *testing.T, ctx context.Context, st *store.Store, runID string, consumed int) {
	t.Helper()
	for {
		node, err := st.GetNode(ctx, runID, "mixed")
		if err == nil && node.ReadyAt != nil && node.AttemptsConsumed == consumed && !node.Claimed {
			if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET offer_started_at = ? WHERE run_id = ? AND node_id = ?`,
				time.Now().Add(-time.Minute).UnixNano(), runID, "mixed"); err != nil {
				t.Fatal(err)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestAutoRetry_RecoversAfterTransientFailures(t *testing.T) {
	autoRetryCount.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "auto-retry-recovers"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); auto-retry should have recovered after 2 transient failures", res.Status, res.Error)
	}
	if got := autoRetryCount.Load(); got != 3 {
		t.Fatalf("dispatch count = %d, want 3 (initial + 2 auto-retries)", got)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	events, err := st.ListEventsAfter(context.Background(), res.RunID, 0, 1000)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	var autoRetryEvents int
	for _, e := range events {
		if e.NodeID == "flaky" && e.Kind == "node_auto_retry" {
			autoRetryEvents++
		}
	}
	if autoRetryEvents != 2 {
		t.Fatalf("node_auto_retry events = %d, want 2 (one per re-dispatch beyond the first)", autoRetryEvents)
	}
	node, err := st.GetNode(context.Background(), res.RunID, "flaky")
	if err != nil {
		t.Fatal(err)
	}
	if node.Outcome != string(sparkwing.Success) {
		t.Fatalf("stored node outcome = %q, want success after RetryAuto recovery", node.Outcome)
	}
}

func TestAutoRetry_WarmRunnerRedispatchesAReportedFailure(t *testing.T) {
	autoRetryCount.Store(0)
	paths := newPaths(t)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := orchestrator.NewControllerServer(t, st, nil)
	defer server.Close()
	ctrl := client.New(server.URL, nil)
	warm := warmpool.New(ctrl, nil, warmpool.Config{
		PollInterval: time.Millisecond, ClaimWaitTimeout: time.Second,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workerDone := make(chan error, 1)
	var bodies atomic.Int32
	go func() {
		for attempt := 1; attempt <= 2; attempt++ {
			var claimed *store.Node
			for claimed == nil {
				n, claimErr := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, fmt.Sprintf("agent:warm:%d", attempt), time.Minute, nil)
				if claimErr == nil {
					claimed = n
					break
				}
				if !errors.Is(claimErr, store.ErrNotFound) {
					workerDone <- claimErr
					return
				}
				select {
				case <-ctx.Done():
					workerDone <- ctx.Err()
					return
				case <-time.After(time.Millisecond):
				}
			}
			bodies.Add(1)
			if attempt == 1 {
				if err := st.FinishNodeWithReason(ctx, claimed.RunID, claimed.NodeID,
					"failed", "ordinary failure", nil, store.FailureVerify, nil); err != nil {
					workerDone <- err
					return
				}
				continue
			}
			workerDone <- st.FinishNode(ctx, claimed.RunID, claimed.NodeID, "success", "", nil)
			return
		}
	}()
	result, err := orchestrator.Run(ctx, orchestrator.LocalBackends(paths, st, nil), orchestrator.Options{
		Pipeline: "auto-retry-recovers", RunID: "warm-auto-retry", Runner: warm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if workerErr := <-workerDone; workerErr != nil {
		t.Fatal(workerErr)
	}
	if result.Status != "success" || bodies.Load() != 2 {
		t.Fatalf("result = %+v, remote bodies = %d; want two dispatches and success", result, bodies.Load())
	}
	node, err := st.GetNode(ctx, result.RunID, "flaky")
	if err != nil {
		t.Fatal(err)
	}
	if node.Outcome != "success" {
		t.Fatalf("stored node = %+v, want final remote success", node)
	}
}

func TestAutoRetry_FallbackThenLegacyLossUsesOnlyTwoGlobalAttempts(t *testing.T) {
	autoRetryCount.Store(0)
	paths := newPaths(t)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := orchestrator.NewControllerServer(t, st, nil)
	defer server.Close()
	ctrl := client.New(server.URL, nil)
	backends := orchestrator.LocalBackends(paths, st, nil)
	warm := warmpool.New(ctrl, orchestrator.NewNodeExecutor(backends), warmpool.Config{
		PollInterval: time.Millisecond, ClaimWaitTimeout: 10 * time.Millisecond,
	}, nil)
	agent := enrollMixedExecutor(t, st)
	ctx, cancel := context.WithTimeout(claimedRetryContext(t, st, "mixed-fallback-agent", "auto-retry-one-failure"), 5*time.Second)
	defer cancel()
	type runResult struct {
		result *orchestrator.Result
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, runErr := orchestrator.Run(ctx, backends, orchestrator.Options{
			Pipeline: "auto-retry-one-failure", RunID: "mixed-fallback-agent", Runner: warm,
		})
		done <- runResult{result: result, err: runErr}
	}()
	claimed := awaitLegacyMixedClaimAfterAttempts(t, ctx, st, "mixed-fallback-agent", 1, agent)
	claimCtx := store.WithNodeClaimFence(store.WithoutClaimFences(ctx), store.NodeClaimFence{
		Claimant: agent, HolderID: claimed.ClaimedBy, MembershipID: claimed.ClaimMembershipID,
		ReservationID: claimed.ReservationID, ClaimGeneration: claimed.ClaimGeneration,
	})
	if err := st.AcknowledgeNodeExecutionStart(claimCtx, claimed.RunID, claimed.NodeID, agent, store.ExecutionStart{
		HolderID: claimed.ClaimedBy, MembershipID: claimed.ClaimMembershipID,
		ReservationID: claimed.ReservationID, ClaimGeneration: claimed.ClaimGeneration, AttemptOrdinal: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().Add(-time.Second).UnixNano(), claimed.RunID, claimed.NodeID); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(st, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID != "" {
		t.Fatalf("recovery = %+v, want exhausted source run", recovered)
	}
	run := <-done
	if run.err != nil {
		t.Fatal(run.err)
	}
	if run.result.Status != "failed" || autoRetryCount.Load() != 1 {
		t.Fatalf("result=%+v fallback bodies=%d", run.result, autoRetryCount.Load())
	}
	node, err := st.GetNode(ctx, "mixed-fallback-agent", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if node.AttemptsConsumed != 2 || len(node.ExecutionAttempts) != 2 || node.FailureReason != store.FailureAgentLost {
		t.Fatalf("mixed node = %+v", node)
	}
}

func TestAutoRetry_AssistedFailureThenFallbackUsesOrdinalTwo(t *testing.T) {
	autoRetryCount.Store(0)
	paths := newPaths(t)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := orchestrator.NewControllerServer(t, st, nil)
	defer server.Close()
	ctrl := client.New(server.URL, nil)
	backends := orchestrator.LocalBackends(paths, st, nil)
	warm := warmpool.New(ctrl, orchestrator.NewNodeExecutor(backends), warmpool.Config{
		PollInterval: time.Millisecond, ClaimWaitTimeout: 10 * time.Millisecond,
	}, nil)
	ctx, cancel := context.WithTimeout(claimedRetryContext(t, st, "mixed-agent-fallback", "auto-retry-one-success"), 5*time.Second)
	defer cancel()
	type runResult struct {
		result *orchestrator.Result
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, runErr := orchestrator.Run(ctx, backends, orchestrator.Options{
			Pipeline: "auto-retry-one-success", RunID: "mixed-agent-fallback", Runner: warm,
		})
		done <- runResult{result: result, err: runErr}
	}()
	agent := store.ClaimIdentity{Principal: "legacy-agent", TokenPrefix: "swr_legacy_agent"}
	claimed := awaitLegacyMixedClaim(t, ctx, st, "mixed-agent-fallback", agent)
	claimCtx := store.WithNodeClaimFence(store.WithoutClaimFences(ctx), store.NodeClaimFence{
		Claimant: agent, HolderID: claimed.ClaimedBy, MembershipID: claimed.ClaimMembershipID,
		ReservationID: claimed.ReservationID, ClaimGeneration: claimed.ClaimGeneration,
	})
	start := store.ExecutionStart{
		HolderID: claimed.ClaimedBy, MembershipID: claimed.ClaimMembershipID,
		ReservationID: claimed.ReservationID, ClaimGeneration: claimed.ClaimGeneration, AttemptOrdinal: 1,
	}
	if err := st.AcknowledgeNodeExecutionStart(claimCtx, claimed.RunID, claimed.NodeID, agent, start); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeExecutionAttempt(claimCtx, claimed.RunID, claimed.NodeID, agent, store.ExecutionAttemptFinish{
		HolderID: claimed.ClaimedBy, MembershipID: claimed.ClaimMembershipID,
		ReservationID: claimed.ReservationID, ClaimGeneration: claimed.ClaimGeneration, AttemptOrdinal: 1,
		Outcome: "failed", FailureReason: store.FailureVerify,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(claimCtx, claimed.RunID, claimed.NodeID,
		"failed", "assisted failure", nil, store.FailureVerify, nil); err != nil {
		t.Fatal(err)
	}
	forceMixedFallback(t, ctx, st, claimed.RunID, 1)
	run := <-done
	if run.err != nil {
		t.Fatal(run.err)
	}
	if run.result.Status != "success" || autoRetryCount.Load() != 1 {
		t.Fatalf("result=%+v fallback bodies=%d", run.result, autoRetryCount.Load())
	}
	node, err := st.GetNode(ctx, "mixed-agent-fallback", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if node.AttemptsConsumed != 2 || len(node.ExecutionAttempts) != 2 ||
		node.ExecutionAttempts[0].Outcome != "failed" || node.ExecutionAttempts[1].Outcome != "success" {
		t.Fatalf("mixed node = %+v", node)
	}
}

func TestAutoRetry_FailsAfterExhaustingAttempts(t *testing.T) {
	autoRetryCount.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "auto-retry-exhausts"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed (auto-retry should not paper over a node that always fails)", res.Status)
	}
	if got := autoRetryCount.Load(); got != 3 {
		t.Fatalf("dispatch count = %d, want 3 (initial + 2 auto-retries before giving up)", got)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	if len(nodes) != 1 || nodes[0].NodeID != "always-fails" {
		t.Fatalf("expected one always-fails node, got %+v", nodes)
	}
	if nodes[0].Outcome != string(sparkwing.Failed) {
		t.Fatalf("node outcome = %q, want failed", nodes[0].Outcome)
	}
	if !strings.Contains(nodes[0].Error, "infra flake") {
		t.Fatalf("node error should cite the underlying error, got %q", nodes[0].Error)
	}
}

func TestAutoRetry_SubtractsCarriedAttemptsFromDispatchBudget(t *testing.T) {
	autoRetryCount.Store(0)
	p := newPaths(t)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	const sourceRunID = "auto-retry-budget-source"
	const runID = "auto-retry-budget-replacement"
	if err := st.CreateRun(ctx, store.Run{ID: sourceRunID, Pipeline: "auto-retry-exhausts", Status: "failed", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: sourceRunID, NodeID: "always-fails", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET attempts_consumed = 1 WHERE run_id = ? AND node_id = ?`, sourceRunID, "always-fails"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(ctx, sourceRunID, "always-fails", "failed", "agent lost", nil, store.FailureAgentLost, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "auto-retry-exhausts", Status: "pending", StartedAt: time.Now(),
		RetryOf: sourceRunID, RetrySource: store.RetrySourceAuto,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO agent_loss_retries
    (run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count)
VALUES (?, ?, ?, ?, ?, ?, ?)`, runID, sourceRunID, sourceRunID, []byte(`["always-fails"]`),
		time.Now().UnixNano(), time.Now().Add(time.Hour).UnixNano(), 1); err != nil {
		t.Fatal(err)
	}
	seedAgentLossRetrySourceFixture(t, st, runID, sourceRunID)
	res, err := orchestrator.RunLocal(ctx, p, orchestrator.Options{
		Pipeline: "auto-retry-exhausts", RunID: runID, State: st,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if got := autoRetryCount.Load(); got != 2 {
		t.Fatalf("dispatch count = %d, want two remaining invocations", got)
	}
	events, err := st.ListEventsAfter(ctx, runID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawGlobalOrdinal bool
	for _, event := range events {
		if event.Kind == "node_auto_retry" && strings.Contains(string(event.Payload), "dispatch 3/3") {
			sawGlobalOrdinal = true
		}
	}
	if !sawGlobalOrdinal {
		t.Fatalf("auto-retry events do not identify global dispatch 3/3: %+v", events)
	}
}
