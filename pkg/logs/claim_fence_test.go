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
	first := offerLogTestClaim(t, st, identity, "source", "build", "agent:a", "reservation-a")
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
	offerLogTestClaim(t, st, identity, retryID, "build", "agent:b", "reservation-b")

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

func offerLogTestClaim(t *testing.T, st *store.Store, identity store.ClaimIdentity, runID, nodeID, holder, reservation string) *store.Node {
	t.Helper()
	const executor = "log-test-agent"
	if err := st.EnrollExecutor(context.Background(), identity.TokenPrefix, store.Executor{
		Name: executor, Kind: "agent", Location: "local", Principal: identity.Principal,
		BasePriority: 100, PriorityCeiling: 100, MaxConcurrent: 1,
		Budget: store.ExecutorResource{Cores: 4, MemoryBytes: 4 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.HeartbeatExecutor(context.Background(), identity, executor,
		store.ExecutorResource{Cores: 4, MemoryBytes: 4 << 30}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	summary, err := st.SchedulingSummary(context.Background(), runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := st.OfferExecutorClaim(context.Background(), identity, store.ExecutorClaimOffer{
		ExecutorName: executor, HolderID: holder, RunID: runID, NodeID: nodeID,
		ReservationID: reservation, ResourceDigest: summary.ResourceDigest, Lease: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if offer.Node == nil {
		t.Fatalf("claim offer remained pending: %+v", offer)
	}
	return offer.Node
}
