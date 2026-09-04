package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSchemaV30CompositeRestoresAgentLossFieldsSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-v29.db")
	ctx := context.Background()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.CreateRun(ctx, store.Run{ID: "preserved", Pipeline: "demo", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "preserved", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE run_definition_plans`,
		`DROP TABLE node_execution_attempts`,
		`DROP TABLE agent_loss_retries`,
		`DROP INDEX idx_triggers_pending`,
		`ALTER TABLE triggers DROP COLUMN available_at`,
		`CREATE INDEX idx_triggers_pending ON triggers(status, created_at) WHERE status = 'pending'`,
		`ALTER TABLE nodes DROP COLUMN required_executor_location`,
		`ALTER TABLE nodes DROP COLUMN required_coordinator_id`,
		`ALTER TABLE nodes DROP COLUMN executor_location`,
		`ALTER TABLE nodes DROP COLUMN retry_root_run_id`,
		`ALTER TABLE nodes DROP COLUMN attempts_consumed`,
		`ALTER TABLE nodes DROP COLUMN claim_membership_id`,
		`ALTER TABLE nodes DROP COLUMN claim_generation`,
		`ALTER TABLE nodes DROP COLUMN avoid_until`,
		`ALTER TABLE nodes DROP COLUMN avoid_executor_id`,
		`ALTER TABLE nodes DROP COLUMN avoid_executor_kind`,
		`ALTER TABLE nodes DROP COLUMN avoid_coordinator_id`,
		`ALTER TABLE nodes DROP COLUMN reservation_id`,
		`ALTER TABLE nodes DROP COLUMN execution_started_at`,
		`ALTER TABLE nodes DROP COLUMN executor_id`,
		`ALTER TABLE nodes DROP COLUMN executor_kind`,
		`ALTER TABLE nodes DROP COLUMN coordinator_id`,
		`ALTER TABLE runs DROP COLUMN retry_avoid_until`,
		`ALTER TABLE runs DROP COLUMN retry_avoid_executor_id`,
		`ALTER TABLE runs DROP COLUMN retry_avoid_executor_kind`,
		`ALTER TABLE runs DROP COLUMN retry_avoid_coordinator_id`,
		`ALTER TABLE runs DROP COLUMN retry_cause_node_id`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 30`,
	} {
		if _, err := st.DB().ExecContext(ctx, stmt); err != nil {
			t.Fatalf("downgrade with %q: %v", stmt, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("open v29 shape at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer up.Close()
	if got := readSchemaVersion(t, up.DB()); got != store.ExpectedSchemaVersion() {
		t.Fatalf("schema version = %d, want %d", got, store.ExpectedSchemaVersion())
	}
	for table, names := range map[string][]string{
		"runs":     {"retry_cause_node_id", "retry_avoid_coordinator_id", "retry_avoid_executor_kind", "retry_avoid_executor_id", "retry_avoid_until"},
		"nodes":    {"coordinator_id", "executor_kind", "executor_id", "execution_started_at", "reservation_id", "avoid_coordinator_id", "avoid_executor_kind", "avoid_executor_id", "avoid_until", "claim_generation", "claim_membership_id", "attempts_consumed", "retry_root_run_id", "executor_location", "required_coordinator_id", "required_executor_location"},
		"triggers": {"available_at"},
	} {
		for _, name := range names {
			var count int
			if err := up.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, name).Scan(&count); err != nil {
				t.Fatalf("inspect %s.%s: %v", table, name, err)
			}
			if count != 1 {
				t.Errorf("migrated schema missing %s.%s", table, name)
			}
		}
	}
	for _, table := range []string{"agent_loss_retries", "node_execution_attempts", "run_definition_plans"} {
		var count int
		if err := up.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("migrated schema missing table %s", table)
		}
	}
	var attemptNameColumn int
	if err := up.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('node_execution_attempts') WHERE name = 'executor_name'`,
	).Scan(&attemptNameColumn); err != nil {
		t.Fatal(err)
	}
	if attemptNameColumn != 1 {
		t.Error("migrated schema missing node_execution_attempts.executor_name")
	}
	if _, err := up.GetNode(ctx, "preserved", "build"); err != nil {
		t.Fatalf("preserved node: %v", err)
	}
}

func TestSchemaV30ExecutionAttemptOrdinalIsClaimBoundAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRetryRunAndReadyNode(t, s, "run-ordinal", 2)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	n := claimNode(t, s, "run-ordinal", identity, "agent:a:1")
	start := store.ExecutionStart{HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, identity, start); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, identity, start); err != nil {
		t.Fatalf("idempotent acknowledgement: %v", err)
	}
	for name, bad := range map[string]store.ExecutionStart{
		"gap":               {HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 3},
		"wrong holder":      {HolderID: "agent:b:1", ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 2},
		"wrong generation":  {HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration + 1, AttemptOrdinal: 2},
		"wrong membership":  {HolderID: n.ClaimedBy, MembershipID: "other", ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 2},
		"wrong reservation": {HolderID: n.ClaimedBy, ReservationID: "other", ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 2},
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, identity, bad); !errors.Is(err, store.ErrLockHeld) {
				t.Fatalf("error = %v, want ErrLockHeld", err)
			}
		})
	}
	stored, err := s.GetNode(ctx, n.RunID, n.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AttemptsConsumed != 1 || len(stored.ExecutionAttempts) != 1 || stored.ExecutionAttempts[0].Attempt != 1 {
		t.Fatalf("attempt state = %+v", stored)
	}
	finish := store.ExecutionAttemptFinish{
		HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1,
		Outcome: "failed", FailureReason: store.FailureVerify,
	}
	if err := s.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, identity, finish); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, identity, finish); err != nil {
		t.Fatalf("idempotent finish: %v", err)
	}
	finish.Outcome = "success"
	if err := s.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, identity, finish); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("conflicting finish = %v, want ErrLockHeld", err)
	}
	stored, err = s.GetNode(ctx, n.RunID, n.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExecutionAttempts[0].FinishedAt == nil || stored.ExecutionAttempts[0].Outcome != "failed" ||
		stored.ExecutionAttempts[0].FailureReason != store.FailureVerify {
		t.Fatalf("finished attempt = %+v", stored.ExecutionAttempts[0])
	}
	start.AttemptOrdinal = 2
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, identity, start); err != nil {
		t.Fatalf("acknowledge replacement attempt: %v", err)
	}
	fence := store.NodeClaimFence{
		Claimant: identity, HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration,
	}
	firstLive, err := s.NodeExecutionAttemptIsLive(ctx, n.RunID, n.NodeID, fence, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	secondLive, err := s.NodeExecutionAttemptIsLive(ctx, n.RunID, n.NodeID, fence, 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if firstLive || !secondLive {
		t.Fatalf("attempt liveness: first=%v second=%v", firstLive, secondLive)
	}
	events, err := s.ListEventsAfter(ctx, n.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	finishedEvents := 0
	for _, event := range events {
		if event.Kind == "execution_attempt_finished" {
			finishedEvents++
		}
	}
	if finishedEvents != 1 {
		t.Fatalf("execution_attempt_finished events = %d, want 1", finishedEvents)
	}
}

func TestSchemaV30TriggerOwnedAttemptUsesTheGlobalOrdinal(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "run-trigger-attempt", Pipeline: "p", Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fenced := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{
		Claimant: identity, ClaimGeneration: trigger.ClaimSeq,
	})
	if err := s.CreateRun(fenced, store.Run{ID: trigger.ID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(fenced, store.Node{RunID: trigger.ID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartNode(fenced, trigger.ID, "build"); err != nil {
		t.Fatal(err)
	}
	start := store.ExecutionStart{ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1}
	if err := s.AcknowledgeNodeExecutionStart(fenced, trigger.ID, "build", identity, start); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeNodeExecutionStart(fenced, trigger.ID, "build", identity, start); err != nil {
		t.Fatalf("idempotent trigger acknowledgement: %v", err)
	}
	node, err := s.GetNode(ctx, trigger.ID, "build")
	if err != nil {
		t.Fatal(err)
	}
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if node.AttemptsConsumed != 1 || len(node.ExecutionAttempts) != 1 ||
		node.ExecutionAttempts[0].CoordinatorID != coordinatorID ||
		node.ExecutionAttempts[0].ExecutorID != coordinatorID ||
		node.ExecutionAttempts[0].ExecutorKind != "" {
		t.Fatalf("trigger attempt = %+v", node.ExecutionAttempts)
	}
	live, err := s.TriggerExecutionAttemptIsLive(ctx, trigger.ID, "build", store.TriggerClaimFence{
		Claimant: identity, ClaimGeneration: trigger.ClaimSeq,
	}, 1, time.Now())
	if err != nil || !live {
		t.Fatalf("live trigger attempt = %v, %v", live, err)
	}
	finish := store.ExecutionAttemptFinish{
		ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1,
		Outcome: "failed", FailureReason: store.FailureVerify,
	}
	if err := s.FinishNodeExecutionAttempt(fenced, trigger.ID, "build", identity, finish); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNodeExecutionAttempt(fenced, trigger.ID, "build", identity, finish); err != nil {
		t.Fatalf("idempotent trigger finish: %v", err)
	}
	live, err = s.TriggerExecutionAttemptIsLive(ctx, trigger.ID, "build", store.TriggerClaimFence{
		Claimant: identity, ClaimGeneration: trigger.ClaimSeq,
	}, 1, time.Now())
	if err != nil || live {
		t.Fatalf("finished trigger attempt live = %v, %v", live, err)
	}
}

func TestSchemaV30FallbackThenAssistedLossCannotExceedRetryBudget(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	triggerIdentity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "run-mixed-budget", Pipeline: "p", Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.ClaimNextTriggerFor(ctx, triggerIdentity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fenced := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{
		Claimant: triggerIdentity, ClaimGeneration: trigger.ClaimSeq,
	})
	plan, _ := json.Marshal(map[string]any{
		"pipeline": "p", "run_id": trigger.ID,
		"nodes": []any{map[string]any{"id": "build", "deps": []string{}, "modifiers": map[string]any{"retry": 1}}},
	})
	if err := s.CreateRun(fenced, store.Run{
		ID: trigger.ID, Pipeline: "p", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
		RepoURL: "https://example.com/acme/repo.git", GitSHA: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(fenced, store.Node{RunID: trigger.ID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartNode(fenced, trigger.ID, "build"); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeNodeExecutionStart(fenced, trigger.ID, "build", triggerIdentity, store.ExecutionStart{
		ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNodeExecutionAttempt(fenced, trigger.ID, "build", triggerIdentity, store.ExecutionAttemptFinish{
		ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1,
		Outcome: "failed", FailureReason: store.FailureVerify,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNodeWithReason(fenced, trigger.ID, "build", "failed", "ordinary failure", nil, store.FailureVerify, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetNodeForAutoRetry(fenced, trigger.ID, "build"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, trigger.ID, "build"); err != nil {
		t.Fatal(err)
	}
	agent := enrollOfferExecutor(t, s, "mixed-agent", 100, 100)
	claimed := executorOffer(t, s, agent, "mixed-agent", "holder-2", "reservation-2", trigger.ID, "build", 0).Node
	if claimed == nil {
		t.Fatal("assisted second attempt was not awarded")
	}
	ackNodeAttempt(t, s, claimed, agent, 2)
	forceExpireNodeClaim(t, s, trigger.ID, "build")
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID != "" {
		t.Fatalf("recovery = %+v, want exhausted without a third run", recovered)
	}
	node, err := s.GetNode(ctx, trigger.ID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if node.AttemptsConsumed != 2 || len(node.ExecutionAttempts) != 2 || node.FailureReason != store.FailureAgentLost {
		t.Fatalf("mixed-mode node = %+v", node)
	}
}

func TestSchemaV30DefinitionPlanHashSurvivesRuntimeExpansionSnapshot(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.CreateRun(ctx, store.Run{ID: "run-plan", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	definition := []byte(`{"pipeline":"p","nodes":[{"id":"discover","deps":[]}]}`)
	expanded := []byte(`{"pipeline":"p","nodes":[{"id":"discover","deps":[]},{"id":"child","deps":["discover"]}]}`)
	if err := s.UpdatePlanSnapshot(ctx, "run-plan", definition); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePlanSnapshot(ctx, "run-plan", expanded); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(definition)
	var got string
	if err := s.DB().QueryRowContext(ctx, `SELECT plan_hash FROM run_definition_plans WHERE run_id = 'run-plan'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "sha256:"+fmt.Sprintf("%x", want[:]) {
		t.Fatalf("definition hash = %q", got)
	}
	run, err := s.GetRun(ctx, "run-plan")
	if err != nil {
		t.Fatal(err)
	}
	if string(run.PlanSnapshot) != string(expanded) {
		t.Fatalf("runtime plan snapshot = %s", run.PlanSnapshot)
	}
}

func TestSchemaV30PreStartLossSchedulesWithoutConsumingBudgetAndHonorsAvailability(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRetryRunAndReadyNode(t, s, "run-prestart", 0)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	claimRetryNode(t, s, "run-prestart", identity, "agent:a:1")
	forceExpireNodeClaim(t, s, "run-prestart", "build")

	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID == "" || recovered[0].Started || recovered[0].Invocations != 0 {
		t.Fatalf("recovery = %+v", recovered)
	}
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("future trigger claim = %v, want ErrNotFound", err)
	}
	retry, err := s.GetRun(ctx, recovered[0].RetryRunID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.RetryOf != "run-prestart" || retry.RetrySource != store.RetrySourceAuto ||
		len(retry.RetryCauseNodeIDs) != 1 || retry.RetryCauseNodeIDs[0] != "build" || retry.RetryAvailableAt == nil {
		t.Fatalf("retry = %+v", retry)
	}
	trigger, err := s.GetTrigger(ctx, retry.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.GetRun(ctx, "run-prestart")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(source.PlanSnapshot)
	if got, want := trigger.TriggerEnv[retryprovenance.PlanHashKey], fmt.Sprintf("sha256:%x", sum); got != want {
		t.Fatalf("retry plan hash = %q, want %q", got, want)
	}
	for _, key := range []string{retryprovenance.RepoDirKey, retryprovenance.RepoIdentityKey, retryprovenance.RevisionKey} {
		if _, exists := trigger.TriggerEnv[key]; exists {
			t.Errorf("remote retry unexpectedly persisted empty workspace key %q", key)
		}
	}
	if err := s.CreateNode(ctx, store.Node{RunID: retry.ID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	retriedNode, err := s.GetNode(ctx, retry.ID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if retriedNode.AttemptsConsumed != 0 || retriedNode.RetryRootRunID != "run-prestart" {
		t.Fatalf("retried node = %+v", retriedNode)
	}
}

func TestSchemaV30RetryAvailabilitySurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	createRetryRunAndReadyNode(t, s, "run-durable-backoff", 0)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	claimRetryNode(t, s, "run-durable-backoff", identity, "agent:a:1")
	forceExpireNodeClaim(t, s, "run-durable-backoff", "build")
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID == "" {
		t.Fatalf("recovery = %+v", recovered)
	}
	retryID := recovered[0].RetryRunID
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("early claim after reopen = %v, want ErrNotFound", err)
	}
	if _, err := s.ClaimSpecificTrigger(ctx, retryID, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("early specific claim after reopen = %v, want ErrNotFound", err)
	}
	if count, err := s.CountPendingTriggers(ctx); err != nil || count != 1 {
		t.Fatalf("pending count during backoff = %d, err=%v, want 1", count, err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE triggers SET available_at = ? WHERE id = ?`,
		time.Now().Add(-time.Second).UnixNano(), retryID); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "run-durable-backoff", "failed", "agent lost"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if count, err := s.CountPendingTriggers(ctx); err != nil || count != 1 {
		t.Fatalf("due claimable count = %d, err=%v, want 1", count, err)
	}
	trigger, err := s.ClaimNextTrigger(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.ID != retryID {
		t.Fatalf("claimed trigger = %q, want %q", trigger.ID, retryID)
	}
}

func TestSchemaV30SequentialLossesJoinPendingRetryAfterSourceQuiesces(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	plan, _ := json.Marshal(map[string]any{
		"pipeline": "p", "run_id": "run-sequential-loss",
		"nodes": []any{
			map[string]any{"id": "a", "deps": []string{}, "modifiers": map[string]any{"retry": 1}},
			map[string]any{"id": "b", "deps": []string{}, "modifiers": map[string]any{"retry": 1}},
		},
	})
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-sequential-loss", Pipeline: "p", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
		RepoURL: "https://example.com/acme/repo.git", GitSHA: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	for _, nodeID := range []string{"a", "b"} {
		if err := s.CreateNode(ctx, store.Node{RunID: "run-sequential-loss", NodeID: nodeID, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkNodeReady(ctx, "run-sequential-loss", nodeID); err != nil {
			t.Fatal(err)
		}
		claimRetryNode(t, s, "run-sequential-loss", identity, "agent:"+nodeID)
	}
	forceExpireNodeClaim(t, s, "run-sequential-loss", "a")
	first, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil || len(first) != 1 || first[0].RetryRunID == "" {
		t.Fatalf("first recovery = %+v, err=%v", first, err)
	}
	retryID := first[0].RetryRunID
	if _, err := s.DB().ExecContext(ctx, `UPDATE triggers SET available_at = ? WHERE id = ?`,
		time.Now().Add(-time.Second).UnixNano(), retryID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextTrigger(ctx, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("retry claimed while source still active: %v", err)
	}
	forceExpireNodeClaim(t, s, "run-sequential-loss", "b")
	second, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil || len(second) != 1 || second[0].RetryRunID != retryID {
		t.Fatalf("second recovery = %+v, err=%v, want retry %s", second, err, retryID)
	}
	retry, err := s.GetRun(ctx, retryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.RetryCauseNodeIDs) != 2 || !slices.Contains(retry.RetryCauseNodeIDs, "a") || !slices.Contains(retry.RetryCauseNodeIDs, "b") {
		t.Fatalf("retry causes = %v, want a and b", retry.RetryCauseNodeIDs)
	}
	if err := s.FinishRun(ctx, "run-sequential-loss", "failed", "agents lost"); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.ClaimNextTrigger(ctx, time.Minute)
	if err != nil || trigger.ID != retryID {
		t.Fatalf("claim after source quiesced = %+v, err=%v", trigger, err)
	}
}

func TestSchemaV30OverduePendingRetryTerminatesBeforeEveryClaimPath(t *testing.T) {
	for _, tc := range []struct {
		name   string
		invoke func(context.Context, *store.Store, string) error
	}{
		{
			name: "general",
			invoke: func(ctx context.Context, s *store.Store, _ string) error {
				_, err := s.ClaimNextTriggerFor(ctx, store.ClaimIdentity{}, time.Minute, nil, nil)
				return err
			},
		},
		{
			name: "parent list",
			invoke: func(ctx context.Context, s *store.Store, _ string) error {
				ids, err := s.ListPendingTriggersForParent(ctx, "parent")
				if err == nil && len(ids) != 0 {
					return fmt.Errorf("pending ids = %v", ids)
				}
				return err
			},
		},
		{
			name: "specific",
			invoke: func(ctx context.Context, s *store.Store, id string) error {
				_, err := s.ClaimSpecificTrigger(ctx, id, time.Minute)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			if err := s.CreateRun(ctx, store.Run{ID: "source", Pipeline: "p", Status: "failed", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateRun(ctx, store.Run{ID: "retry", Pipeline: "p", Status: "pending", StartedAt: time.Now(), RetryOf: "source", RetrySource: store.RetrySourceAuto}); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateTrigger(ctx, store.Trigger{
				ID: "retry", Pipeline: "p", Status: "pending", ParentRunID: "parent",
				RetryOf: "source", RetrySource: store.RetrySourceAuto, CreatedAt: time.Now().Add(-time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().ExecContext(ctx, `INSERT INTO agent_loss_retries
    (run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count)
VALUES (?, ?, ?, ?, ?, ?, ?)`, "retry", "source", "source", []byte(`["build"]`),
				time.Now().Add(-time.Hour).UnixNano(), time.Now().Add(-time.Second).UnixNano(), 1); err != nil {
				t.Fatal(err)
			}
			err := tc.invoke(ctx, s, "retry")
			if tc.name != "parent list" && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("claim = %v, want ErrNotFound", err)
			}
			if err != nil && tc.name == "parent list" {
				t.Fatal(err)
			}
			run, err := s.GetRun(ctx, "retry")
			if err != nil {
				t.Fatal(err)
			}
			trigger, err := s.GetTrigger(ctx, "retry")
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != "failed" || !strings.Contains(run.Error, "deadline exceeded") || trigger.Status != "done" {
				t.Fatalf("expired retry state: run=%+v trigger=%+v", run, trigger)
			}
			events, err := s.ListEventsAfter(ctx, "retry", 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Kind != "agent_loss_retry_deadline_exceeded" {
				t.Fatalf("retry events = %+v", events)
			}
		})
	}
}

func TestSchemaV30RetryOneSecondLossCannotExceedTwoInvocations(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRetryRunAndReadyNode(t, s, "run-budget", 1)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	replacementIdentity := enrollOfferExecutor(t, s, "budget-replacement", 100, 100)
	first := claimRetryNode(t, s, "run-budget", identity, "agent:a:1")
	ackNodeAttempt(t, s, first, identity, 1)
	forceExpireNodeClaim(t, s, first.RunID, first.NodeID)
	firstRecovery, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	retryID := firstRecovery[0].RetryRunID
	if retryID == "" {
		t.Fatalf("first recovery = %+v", firstRecovery)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE runs SET status = 'running' WHERE id = ?`, retryID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: retryID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, retryID, "build"); err != nil {
		t.Fatal(err)
	}
	second := executorOffer(t, s, replacementIdentity, "budget-replacement", "agent:b:1", "reservation-b", retryID, "build", 0).Node
	if second == nil {
		t.Fatal("replacement executor was not awarded the retry node")
	}
	if second.AttemptsConsumed != 1 {
		t.Fatalf("second claim consumed = %d, want 1", second.AttemptsConsumed)
	}
	ackNodeAttempt(t, s, second, replacementIdentity, 2)
	forceExpireNodeClaim(t, s, retryID, "build")
	secondRecovery, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondRecovery) != 1 || secondRecovery[0].RetryRunID != "" || secondRecovery[0].Invocations != 2 {
		t.Fatalf("second recovery = %+v", secondRecovery)
	}
	attempts, err := s.ListNodeExecutionAttempts(ctx, retryID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Attempt != 1 || attempts[1].Attempt != 2 {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestSchemaV30RetryAutoResetMakesTheNextUnstartedLossPreStart(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRetryRunAndReadyNode(t, s, "run-reset-prestart", 1)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	replacementIdentity := enrollOfferExecutor(t, s, "reset-replacement", 100, 100)
	first := claimRetryNode(t, s, "run-reset-prestart", identity, "agent:a:1")
	start := store.ExecutionStart{
		HolderID: first.ClaimedBy, MembershipID: first.ClaimMembershipID,
		ReservationID: first.ReservationID, ClaimGeneration: first.ClaimGeneration, AttemptOrdinal: 1,
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, first.RunID, first.NodeID, identity, start); err != nil {
		t.Fatal(err)
	}
	finish := store.ExecutionAttemptFinish{
		HolderID: start.HolderID, MembershipID: start.MembershipID, ReservationID: start.ReservationID,
		ClaimGeneration: start.ClaimGeneration, AttemptOrdinal: start.AttemptOrdinal,
		Outcome: "failed", FailureReason: store.FailureUnknown,
	}
	if err := s.FinishNodeExecutionAttempt(ctx, first.RunID, first.NodeID, identity, finish); err != nil {
		t.Fatal(err)
	}
	fenced := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: identity, HolderID: first.ClaimedBy, MembershipID: first.ClaimMembershipID,
		ReservationID: first.ReservationID, ClaimGeneration: first.ClaimGeneration,
	})
	if err := s.FinishNodeWithReason(fenced, first.RunID, first.NodeID, "failed", "ordinary failure", nil, store.FailureUnknown, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetNodeForAutoRetry(ctx, first.RunID, first.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, first.RunID, first.NodeID); err != nil {
		t.Fatal(err)
	}
	second := executorOffer(t, s, replacementIdentity, "reset-replacement", "agent:b:1", "reservation-b", first.RunID, first.NodeID, 0).Node
	if second == nil {
		t.Fatal("replacement executor was not awarded the reset node")
	}
	if second.AttemptsConsumed != 1 || second.ExecutionStartedAt != nil {
		t.Fatalf("reset claim state = %+v", second)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().Add(-time.Second).UnixNano(), second.RunID, second.NodeID); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].Started || recovered[0].Invocations != 1 || recovered[0].RetryRunID == "" {
		t.Fatalf("recovery = %+v", recovered)
	}
	events, err := s.ListEventsAfter(ctx, second.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawPrestart bool
	for _, event := range events {
		if event.Kind == "agent_loss_prestart_requeued" {
			sawPrestart = true
		}
	}
	if !sawPrestart {
		t.Fatalf("events = %+v, want agent_loss_prestart_requeued", events)
	}
}

func TestSchemaV30StaleClaimCannotMutateAfterRetryAward(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRetryRunAndReadyNode(t, s, "run-fence", 1)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	replacementIdentity := enrollOfferExecutor(t, s, "fence-replacement", 100, 100)
	first := claimRetryNode(t, s, "run-fence", identity, "agent:a:1")
	ackNodeAttempt(t, s, first, identity, 1)
	staleCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: identity, HolderID: first.ClaimedBy, ClaimGeneration: first.ClaimGeneration,
	})
	forceExpireNodeClaim(t, s, first.RunID, first.NodeID)
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	retryID := recovered[0].RetryRunID
	if _, err := s.DB().ExecContext(ctx, `UPDATE runs SET status = 'running' WHERE id = ?`, retryID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: retryID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, retryID, "build"); err != nil {
		t.Fatal(err)
	}
	if replacement := executorOffer(t, s, replacementIdentity, "fence-replacement", "agent:b:1", "reservation-b", retryID, "build", 0).Node; replacement == nil {
		t.Fatal("replacement executor was not awarded the retry node")
	}

	checks := []struct {
		name string
		fn   func() error
	}{
		{"start", func() error { return s.StartNode(staleCtx, first.RunID, first.NodeID) }},
		{"finish", func() error { return s.FinishNode(staleCtx, first.RunID, first.NodeID, "success", "", nil) }},
		{"status", func() error { return s.SetNodeStatus(staleCtx, first.RunID, first.NodeID, "running") }},
		{"deps", func() error { return s.UpdateNodeDeps(staleCtx, first.RunID, first.NodeID, []string{"stale"}) }},
		{"artifact", func() error { return s.SetNodeArtifactManifest(staleCtx, first.RunID, first.NodeID, "sha256:stale") }},
		{"step", func() error { return s.StartNodeStep(staleCtx, first.RunID, first.NodeID, "publish") }},
		{"metric", func() error {
			return s.AddNodeMetricSample(staleCtx, first.RunID, first.NodeID, store.MetricSample{TS: time.Now(), CPUMillicores: 1})
		}},
		{"annotation", func() error { return s.AppendNodeAnnotation(staleCtx, first.RunID, first.NodeID, "stale") }},
		{"event", func() error {
			_, err := s.AppendEvent(staleCtx, first.RunID, first.NodeID, "stale_event", nil)
			return err
		}},
	}
	for _, check := range checks {
		if err := check.fn(); !errors.Is(err, store.ErrLockHeld) {
			t.Errorf("stale %s = %v, want ErrLockHeld", check.name, err)
		}
	}
	source, err := s.GetNode(ctx, first.RunID, first.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if source.FailureReason != store.FailureAgentLost || source.ArtifactManifest != "" {
		t.Fatalf("source node = %+v", source)
	}
	steps, err := s.ListNodeSteps(ctx, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Fatalf("stale steps = %+v", steps)
	}
	metrics, err := s.ListNodeMetrics(ctx, first.RunID, first.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 0 || len(source.Annotations) != 0 {
		t.Fatalf("stale writes survived: metrics=%+v annotations=%+v", metrics, source.Annotations)
	}
	events, err := s.ListEventsAfter(ctx, first.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "stale_event" {
			t.Fatal("stale execution event was persisted")
		}
	}
}

func TestSchemaV30TriggerMutationFenceIsAtomicAndRunBound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	identity := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_runner_a"}
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "trigger-run", Pipeline: "p", Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{trigger.ID, "other-run"} {
		if err := s.CreateRun(ctx, store.Run{ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	fenced := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{
		Claimant: identity, ClaimGeneration: trigger.ClaimSeq,
	})
	if err := s.SetNodeSummary(fenced, trigger.ID, "build", "live"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeSummary(fenced, "other-run", "build", "cross-run"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("cross-run mutation = %v, want ErrLockHeld", err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE triggers SET lease_expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Second).UnixNano(), trigger.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeSummary(fenced, trigger.ID, "build", "expired"); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("expired mutation = %v, want ErrLockHeld", err)
	}
	n, err := s.GetNode(ctx, trigger.ID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if n.Summary != "live" {
		t.Fatalf("summary = %q, want live", n.Summary)
	}
}

func TestSchemaV30AgentLossEventsAreOrderedAndCredentialSafe(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRetryRunAndReadyNode(t, s, "run-events", 1)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_secret_prefix"}
	n := claimRetryNode(t, s, "run-events", identity, "agent:a:secret-holder")
	ackNodeAttempt(t, s, n, identity, 1)
	forceExpireNodeClaim(t, s, n.RunID, n.NodeID)
	if _, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListEventsAfter(ctx, n.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, event := range events {
		if strings.HasPrefix(event.Kind, "execution_attempt") || strings.HasPrefix(event.Kind, "agent_") {
			kinds = append(kinds, event.Kind)
			raw, _ := json.Marshal(event.Payload)
			if strings.Contains(string(raw), identity.TokenPrefix) || strings.Contains(string(raw), n.ClaimedBy) ||
				strings.Contains(string(raw), "coordinator_id") || strings.Contains(string(raw), "membership_id") ||
				strings.Contains(string(raw), "executor_id") || strings.Contains(string(raw), "reservation_id") ||
				strings.Contains(string(raw), "holder_id") || strings.Contains(string(raw), "token_prefix") {
				t.Fatalf("credential leaked in %s: %s", event.Kind, raw)
			}
		}
	}
	want := []string{"execution_attempt_started", "agent_lease_lost", "agent_loss_poststart_retry_scheduled"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
}

func TestSchemaV30SoftAvoidancePrefersAnotherEnrolledExecutor(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := enrollOfferExecutor(t, s, "desk-a", 50, 50)
	b := enrollOfferExecutor(t, s, "desk-b", 50, 50)
	var avoidedExecutorID string
	if err := s.DB().QueryRowContext(ctx, `SELECT executor_id FROM executors WHERE name = 'desk-a'`).Scan(&avoidedExecutorID); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Minute)
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-avoid", Pipeline: "p", Status: "running", StartedAt: time.Now(),
		RetryAvoidCoordinatorID: coordinatorID, RetryAvoidExecutorKind: "agent",
		RetryAvoidExecutorID: avoidedExecutorID, RetryAvoidUntil: &until,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-avoid", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run-avoid", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TestOnlyPrepareNextExecutorClaim(ctx, a, "desk-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("avoided executor preparation = %v, want ErrNotFound", err)
	}
	preparation, err := s.TestOnlyPrepareNextExecutorClaim(ctx, b, "desk-b")
	if err != nil {
		t.Fatal(err)
	}
	if preparation.Membership.WorkerID != "desk-b" || preparation.Summary.NodeID != "build" {
		t.Fatalf("alternate preparation = %+v", preparation)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE executors SET last_seen = ? WHERE name = 'desk-b'`,
		time.Now().Add(-2*store.ExecutorRegistrationActiveWindow).UnixNano()); err != nil {
		t.Fatal(err)
	}
	preparation, err = s.TestOnlyPrepareNextExecutorClaim(ctx, a, "desk-a")
	if err != nil {
		t.Fatalf("only viable avoided executor should reclaim: %v", err)
	}
	if preparation.Membership.WorkerID != "desk-a" {
		t.Fatalf("fallback preparation = %+v", preparation)
	}
}

func TestSchemaV30RetryPreservesControllerOwnedPlacement(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	local := enrollOfferExecutor(t, s, "local-box", 100, 100)
	cloud := enrollOfferExecutor(t, s, "cloud-box", 100, 100)
	if err := s.EnrollExecutor(ctx, cloud.TokenPrefix, store.Executor{
		Name: "cloud-box", Kind: "agent", Location: "cloud", Principal: cloud.Principal,
		BasePriority: 100, PriorityCeiling: 100, MaxConcurrent: 2,
		Budget: store.ExecutorResource{Cores: 8, MemoryBytes: 16 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	createRetryRunAndReadyNode(t, s, "run-placement", 1)
	first := executorOffer(t, s, local, "local-box", "holder-a", "reservation-a", "run-placement", "build", 0).Node
	if first == nil || first.RequiredExecutorLocation != "local" || first.RequiredCoordinatorID == "" {
		t.Fatalf("source placement = %+v", first)
	}
	ackNodeAttempt(t, s, first, local, 1)
	forceExpireNodeClaim(t, s, first.RunID, first.NodeID)
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	retryID := recovered[0].RetryRunID
	if _, err := s.DB().ExecContext(ctx, `UPDATE runs SET status = 'running' WHERE id = ?`, retryID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: retryID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, retryID, "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "legacy-holder", time.Minute, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("legacy claim bypassed hard placement: %v", err)
	}
	if _, err := s.TestOnlyPrepareNextExecutorClaim(ctx, cloud, "cloud-box"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cloud placement = %v, want ErrNotFound", err)
	}
	preparation, err := s.TestOnlyPrepareNextExecutorClaim(ctx, local, "local-box")
	if err != nil {
		t.Fatal(err)
	}
	if preparation.Summary.RequiredLocation != "local" || preparation.Summary.RequiredCoordinatorID != first.RequiredCoordinatorID {
		t.Fatalf("retry placement = %+v", preparation.Summary)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE nodes SET offer_started_at = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().Add(-time.Minute).UnixNano(), retryID, "build"); err != nil {
		t.Fatal(err)
	}
	round, err := s.FinalizeExecutorClaimRound(ctx, retryID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if round.Revoked || !round.Pending {
		t.Fatalf("required placement fallback = %+v, want node held for an eligible enrolled executor", round)
	}
	held, err := s.GetNode(ctx, retryID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if held.ReadyAt == nil {
		t.Fatal("required placement was relaxed to coordinator fallback")
	}
}

func TestSchemaV30UnknownRequiredPlacementFailsClosed(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	identity := enrollOfferExecutor(t, s, "mystery", 100, 100)
	if err := s.EnrollExecutor(ctx, identity.TokenPrefix, store.Executor{
		Name: "mystery", Kind: "agent", Location: "unknown", Principal: identity.Principal,
		BasePriority: 100, PriorityCeiling: 100, MaxConcurrent: 2,
		Budget: store.ExecutorResource{Cores: 8, MemoryBytes: 16 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	createRetryRunAndReadyNode(t, s, "run-unknown-placement", 1)
	first := executorOffer(t, s, identity, "mystery", "holder", "reservation", "run-unknown-placement", "build", 0).Node
	ackNodeAttempt(t, s, first, identity, 1)
	forceExpireNodeClaim(t, s, first.RunID, first.NodeID)
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if recovered[0].RetryRunID != "" {
		t.Fatalf("unknown placement retry = %+v", recovered)
	}
	events, err := s.ListEventsAfter(ctx, first.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "agent_loss_retry_not_scheduled" && strings.Contains(string(event.Payload), "placement_unknown") {
			return
		}
	}
	t.Fatal("missing placement_unknown retry decision")
}

func TestSchemaV30LegacyClaimWithoutTrustedPlacementDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRetryRunAndReadyNode(t, s, "run-legacy-placement", 1)
	identity := store.ClaimIdentity{Principal: "legacy-runner", TokenPrefix: "swr_legacy"}
	n := claimNode(t, s, "run-legacy-placement", identity, "legacy-holder")
	ackNodeAttempt(t, s, n, identity, 1)
	forceExpireNodeClaim(t, s, n.RunID, n.NodeID)
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID != "" {
		t.Fatalf("legacy placement recovery = %+v, want no automatic retry", recovered)
	}
	events, err := s.ListEventsAfter(ctx, n.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "agent_loss_retry_not_scheduled" && strings.Contains(string(event.Payload), "placement_unknown") {
			return
		}
	}
	t.Fatal("missing placement_unknown decision for legacy claim")
}

func TestSchemaV30ConcurrentLossCoalescesRetryAndRecordsMixedBudgetDecision(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	plan, _ := json.Marshal(map[string]any{
		"pipeline": "p", "run_id": "run-mixed",
		"nodes": []any{
			map[string]any{"id": "exhausted", "deps": []string{}, "modifiers": map[string]any{"retry": 0}},
			map[string]any{"id": "retryable", "deps": []string{}, "modifiers": map[string]any{"retry": 1}},
		},
	})
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-mixed", Pipeline: "p", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
		RepoURL: "https://example.com/acme/repo.git", GitSHA: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	for _, nodeID := range []string{"exhausted", "retryable"} {
		if err := s.CreateNode(ctx, store.Node{RunID: "run-mixed", NodeID: nodeID, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkNodeReady(ctx, "run-mixed", nodeID); err != nil {
			t.Fatal(err)
		}
		n := claimRetryNode(t, s, "run-mixed", identity, "agent:"+nodeID)
		ackNodeAttempt(t, s, n, identity, 1)
		forceExpireNodeClaim(t, s, n.RunID, n.NodeID)
	}
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 2 {
		t.Fatalf("recoveries = %+v", recovered)
	}
	retryIDs := map[string]string{}
	for _, item := range recovered {
		retryIDs[item.NodeID] = item.RetryRunID
	}
	if retryIDs["exhausted"] != "" || retryIDs["retryable"] == "" {
		t.Fatalf("retry decisions = %+v", retryIDs)
	}
	events, err := s.ListEventsAfter(ctx, "run-mixed", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[string]string{}
	for _, event := range events {
		if event.Kind == "agent_loss_retry_exhausted" || event.Kind == "agent_loss_poststart_retry_scheduled" {
			decisions[event.NodeID] = event.Kind
		}
	}
	if decisions["exhausted"] != "agent_loss_retry_exhausted" || decisions["retryable"] != "agent_loss_poststart_retry_scheduled" {
		t.Fatalf("decision events = %+v", decisions)
	}
}

func TestSchemaV30InvalidPlanIsNotReportedAsBudgetExhaustion(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-invalid-plan", Pipeline: "p", Status: "running", StartedAt: time.Now(), PlanSnapshot: []byte(`{"nodes":`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-invalid-plan", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run-invalid-plan", "build"); err != nil {
		t.Fatal(err)
	}
	claimRetryNode(t, s, "run-invalid-plan", store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}, "agent:a")
	forceExpireNodeClaim(t, s, "run-invalid-plan", "build")
	if _, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListEventsAfter(ctx, "run-invalid-plan", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "agent_loss_retry_exhausted" {
			t.Fatal("invalid plan was reported as budget exhaustion")
		}
		if event.Kind == "agent_loss_retry_not_scheduled" && strings.Contains(string(event.Payload), "invalid_plan") {
			return
		}
	}
	t.Fatal("missing invalid-plan retry decision event")
}

func TestSchemaV30MissingRetryProvenanceIsExplicit(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	plan, _ := json.Marshal(map[string]any{
		"pipeline": "p", "run_id": "run-missing-provenance",
		"nodes": []any{map[string]any{"id": "build", "modifiers": map[string]any{"retry": 1}}},
	})
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-missing-provenance", Pipeline: "p", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-missing-provenance", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run-missing-provenance", "build"); err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	n := claimRetryNode(t, s, "run-missing-provenance", identity, "agent:a:1")
	ackNodeAttempt(t, s, n, identity, 1)
	forceExpireNodeClaim(t, s, n.RunID, n.NodeID)
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID != "" {
		t.Fatalf("recovery = %+v", recovered)
	}
	events, err := s.ListEventsAfter(ctx, n.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "agent_loss_retry_not_scheduled" && strings.Contains(string(event.Payload), "missing_provenance") {
			return
		}
	}
	t.Fatal("missing explicit missing-provenance retry decision event")
}

func TestSchemaV30WorkingTreeRetryKeepsImmutableProvenance(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	plan, _ := json.Marshal(map[string]any{
		"pipeline": "p", "run_id": "run-workspace",
		"nodes": []any{map[string]any{"id": "build", "modifiers": map[string]any{"retry": 1}}},
	})
	repoDir := filepath.Join(t.TempDir(), "repo")
	revision := strings.Repeat("b", 40)
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-workspace", Pipeline: "p", Status: "running", StartedAt: time.Now(),
		TriggerSource: "pipeline-working-tree@laptop", PlanSnapshot: plan,
		RepoURL: "https://example.com/acme/repo.git", GitSHA: revision,
		Invocation: map[string]any{"retry_provenance": map[string]any{
			"repo_dir": repoDir, "repo_identity": "https://example.com/acme/repo.git",
			"revision": revision, "content_policy": retryprovenance.RecordedRevisionSnapshotPolicy,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-workspace", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run-workspace", "build"); err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	claimRetryNode(t, s, "run-workspace", identity, "agent:a:1")
	forceExpireNodeClaim(t, s, "run-workspace", "build")
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID == "" {
		t.Fatalf("recovery = %+v", recovered)
	}
	trigger, err := s.GetTrigger(ctx, recovered[0].RetryRunID)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.TriggerSource != "pipeline-working-tree@laptop" ||
		trigger.TriggerEnv[retryprovenance.RepoDirKey] != repoDir ||
		trigger.TriggerEnv[retryprovenance.RepoIdentityKey] != "https://example.com/acme/repo.git" ||
		trigger.TriggerEnv[retryprovenance.RevisionKey] != revision {
		t.Fatalf("retry provenance = source:%q env:%+v", trigger.TriggerSource, trigger.TriggerEnv)
	}
	if got, want := trigger.TriggerEnv[retryprovenance.PlanHashKey], fmt.Sprintf("sha256:%x", sha256.Sum256(plan)); got != want {
		t.Fatalf("retry plan hash = %q, want %q", got, want)
	}
}

func createRetryRunAndReadyNode(t *testing.T, s *store.Store, runID string, retries int) {
	t.Helper()
	plan, _ := json.Marshal(map[string]any{
		"pipeline": "p", "run_id": runID,
		"nodes": []any{map[string]any{"id": "build", "deps": []string{}, "modifiers": map[string]any{"retry": retries}}},
	})
	if err := s.CreateRun(context.Background(), store.Run{
		ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
		RepoURL: "https://example.com/acme/repo.git", GitSHA: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(context.Background(), store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(context.Background(), runID, "build"); err != nil {
		t.Fatal(err)
	}
}

func claimNode(t *testing.T, s *store.Store, runID string, identity store.ClaimIdentity, holder string) *store.Node {
	t.Helper()
	n, err := s.ClaimNextReadyNode(context.Background(), identity, holder, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n.RunID != runID {
		t.Fatalf("claimed run %q, want %q", n.RunID, runID)
	}
	return n
}

func claimRetryNode(t *testing.T, s *store.Store, runID string, identity store.ClaimIdentity, holder string) *store.Node {
	t.Helper()
	n := claimNode(t, s, runID, identity, holder)
	coordinatorID, err := s.CoordinatorID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(context.Background(), `UPDATE nodes
   SET required_coordinator_id = ?, required_executor_location = 'local', executor_location = 'local'
 WHERE run_id = ? AND node_id = ?`, coordinatorID, n.RunID, n.NodeID); err != nil {
		t.Fatal(err)
	}
	n.RequiredCoordinatorID = coordinatorID
	n.RequiredExecutorLocation = "local"
	n.ExecutorLocation = "local"
	return n
}

func ackNodeAttempt(t *testing.T, s *store.Store, n *store.Node, identity store.ClaimIdentity, ordinal int) {
	t.Helper()
	if err := s.AcknowledgeNodeExecutionStart(context.Background(), n.RunID, n.NodeID, identity, store.ExecutionStart{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID:   n.ReservationID,
		ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: ordinal,
	}); err != nil {
		t.Fatal(err)
	}
}

func forceExpireNodeClaim(t *testing.T, s *store.Store, runID, nodeID string) {
	t.Helper()
	if _, err := s.DB().ExecContext(context.Background(), `UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().Add(-time.Second).UnixNano(), runID, nodeID); err != nil {
		t.Fatal(err)
	}
}
