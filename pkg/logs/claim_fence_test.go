package logs_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestLogs_StaleNodeClaimCannotAppendAfterRetryAward(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw, token, err := st.CreateToken("runner", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim, controller.ScopeLogsRead, controller.ScopeLogsWrite}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: token.Principal, TokenPrefix: token.Prefix}
	plan, _ := json.Marshal(map[string]any{
		"pipeline": "demo", "nodes": []any{map[string]any{"id": "build", "modifiers": map[string]any{"retry": 1}}},
	})
	if err := st.CreateRun(ctx, store.Run{
		ID: "source", Pipeline: "demo", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
		RepoURL: "https://example.com/acme/repo.git", GitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "source", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "source", "build"); err != nil {
		t.Fatal(err)
	}
	first := seedHistoricalLogTestClaim(t, st, identity, "source", "build", "agent:a", "reservation-a")
	staleCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: identity, HolderID: first.ClaimedBy, MembershipID: first.ClaimMembershipID,
		ReservationID: first.ReservationID, ClaimGeneration: first.ClaimGeneration,
	})
	if err := st.AcknowledgeNodeExecutionStart(ctx, "source", "build", identity, store.ExecutionStart{
		HolderID: first.ClaimedBy, MembershipID: first.ClaimMembershipID, ReservationID: first.ReservationID,
		ClaimGeneration: first.ClaimGeneration, AttemptOrdinal: 1,
	}); err != nil {
		t.Fatal(err)
	}
	staleCtx = store.WithExecutionAttemptOrdinal(staleCtx, 1)

	controllerServer := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer controllerServer.Close()
	logServer, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	logsHTTP := httptest.NewServer(logServer.WithControllerAuth(controllerServer.URL, 0).Handler())
	defer logsHTTP.Close()
	logClient := logs.NewClientWithToken(logsHTTP.URL, nil, raw)
	if err := logClient.Append(staleCtx, "source", "build", []byte("before\n")); err != nil {
		t.Fatal(err)
	}

	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET lease_expires_at = ? WHERE run_id = 'source' AND node_id = 'build'`, time.Now().Add(-time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(st, ctx)
	if err != nil {
		t.Fatal(err)
	}
	retryID := recovered[0].RetryRunID
	if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET status = 'running' WHERE id = ?`, retryID); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: retryID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, retryID, "build"); err != nil {
		t.Fatal(err)
	}
	seedHistoricalLogTestClaim(t, st, identity, retryID, "build", "agent:b", "reservation-b")

	if err := logClient.Append(staleCtx, "source", "build", []byte("stale\n")); !errors.Is(err, logs.ErrClaimConflict) {
		t.Fatalf("stale append = %v, want ErrClaimConflict", err)
	}
	body, err := logClient.Read(ctx, "source", "build")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "before\n" {
		t.Fatalf("source log = %q, want only pre-loss bytes", body)
	}
}

func seedHistoricalLogTestClaim(t *testing.T, st *store.Store, identity store.ClaimIdentity, runID, nodeID, holder, reservation string) *store.Node {
	t.Helper()
	coordinatorID, err := st.CoordinatorID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// safety: schema 31 refuses unsealed assisted offers; this fixture restores
	// only the historical attribution needed to exercise retry claim fencing.
	result, err := st.DB().ExecContext(context.Background(), `UPDATE nodes
SET claimed_by = ?, claim_principal = ?, claim_token_prefix = ?,
    claim_executor = 'log-test-agent', claim_reservation = ?, claim_slot = 0,
    lease_expires_at = ?, coordinator_id = ?, executor_kind = 'agent',
    executor_id = 'log-test-agent', executor_location = 'local',
    claim_worker_id = 'log-test-agent', claim_membership_id = 'log-test-membership', reservation_id = ?,
    required_coordinator_id = ?, required_executor_location = 'local',
    claim_generation = claim_generation + 1
WHERE run_id = ? AND node_id = ? AND ready_at IS NOT NULL AND claimed_by IS NULL`,
		holder, identity.Principal, identity.TokenPrefix, reservation, time.Now().Add(time.Minute).UnixNano(),
		coordinatorID, reservation, coordinatorID, runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("seed historical claim rows = %d, %v", changed, err)
	}
	claimed, err := st.GetNode(context.Background(), runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return claimed
}
