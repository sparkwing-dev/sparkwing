package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedRunNode(t *testing.T, st *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
}

func setNodeReadyAt(t *testing.T, st *store.Store, runID, nodeID string, at time.Time) {
	t.Helper()
	res, err := st.DB().Exec(
		`UPDATE nodes SET ready_at = ? WHERE run_id = ? AND node_id = ?`,
		at.UnixNano(), runID, nodeID,
	)
	if err != nil {
		t.Fatalf("set ready_at: %v", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("count ready_at rows: %v", err)
	}
	if changed != 1 {
		t.Fatalf("ready_at rows = %d, want 1", changed)
	}
}

func TestNodeClaim_HTTPRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	n, err := c.ClaimNode(ctx, "pod-1", nil, 30*time.Second, nil)
	if err != nil || n != nil {
		t.Fatalf("expected (nil, nil) on empty queue, got (%v, %v)", n, err)
	}

	seedRunNode(t, st, "run-1", "node-a")
	if err := c.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	n, err = c.ClaimNode(ctx, "pod-1", nil, 30*time.Second, nil)
	if err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}
	if n == nil || n.RunID != "run-1" || n.NodeID != "node-a" {
		t.Fatalf("wrong claim response: %+v", n)
	}
	if n.ClaimedBy != "pod-1" {
		t.Fatalf("claimed_by: %q", n.ClaimedBy)
	}
	visible, err := c.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if !visible.Claimed || visible.ClaimedBy != "" {
		t.Fatalf("ordinary node response exposed holder or lost claim state: %+v", visible)
	}
	claimCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})

	if err := c.HeartbeatNodeClaim(claimCtx, "run-1", "node-a", "pod-1", 30*time.Second, nil); err != nil {
		t.Fatalf("HeartbeatNodeClaim (holder): %v", err)
	}

	err = c.HeartbeatNodeClaim(claimCtx, "run-1", "node-a", "pod-2", 30*time.Second, nil)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got %v", err)
	}

	revoked, err := c.RevokeNodeReady(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatalf("RevokeNodeReady: %v", err)
	}
	if revoked {
		t.Fatal("revoke should be false when node is claimed")
	}
}

func TestNodeClaim_ExecutionStartAcknowledgement(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()
	seedRunNode(t, st, "run-start", "build")
	if err := c.MarkNodeReady(ctx, "run-start", "build"); err != nil {
		t.Fatal(err)
	}
	n, err := c.ClaimNodeAs(ctx, "gateway:edge-a:1", nil, time.Minute, nil, store.ExecutorIdentity{
		Kind: "gateway", ID: "edge-a", ReservationID: "downstream-17",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, store.ExecutionStart{HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetNode(ctx, n.RunID, n.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionStartedAt == nil || got.ExecutorKind != "" || got.ExecutorID != "" || got.ReservationID != "" || got.ExecutorLocation != "unknown" {
		t.Fatalf("execution attempt = %+v", got)
	}
	if err := c.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, store.ExecutionStart{HolderID: "gateway:edge-b:1", ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("wrong holder acknowledgement = %v, want ErrLockHeld", err)
	}
	invalid := store.ExecutionAttemptFinish{
		HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1,
		Outcome: "failed", FailureReason: "credential-shaped-free-text",
	}
	if err := c.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, invalid); err == nil || errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("unstructured execution finish = %v, want bad request", err)
	}
	finish := store.ExecutionAttemptFinish{
		HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1,
		Outcome: "failed", FailureReason: store.FailureVerify,
	}
	if err := c.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, finish); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, finish); err != nil {
		t.Fatalf("idempotent execution finish: %v", err)
	}
	finish.Outcome = "success"
	finish.FailureReason = ""
	if err := c.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, finish); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("conflicting execution finish = %v, want ErrLockHeld", err)
	}
	if err := c.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, store.ExecutionStart{
		HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, store.ExecutionAttemptFinish{
		HolderID: n.ClaimedBy, ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 2,
		Outcome: "cancelled",
	}); err != nil {
		t.Fatalf("cancelled execution finish: %v", err)
	}
}

func TestNodeClaim_RevokeAfterReadyNoPodClaimedYet(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	seedRunNode(t, st, "run-1", "node-a")
	if err := c.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	revoked, err := c.RevokeNodeReady(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("revoke should succeed on ready, unclaimed node")
	}
	n, err := c.ClaimNode(ctx, "pod-1", nil, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != nil {
		t.Fatalf("node should not be claimable after revoke: %+v", n)
	}
}

func TestNodeClaim_HTTPLabelFiltering(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "run-1", NodeID: "special", Status: "pending", NeedsLabels: []string{"special"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "run-1", NodeID: "anyone", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkNodeReady(ctx, "run-1", "special"); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkNodeReady(ctx, "run-1", "anyone"); err != nil {
		t.Fatal(err)
	}
	setNodeReadyAt(t, st, "run-1", "special", time.Unix(100, 0))
	setNodeReadyAt(t, st, "run-1", "anyone", time.Unix(200, 0))

	n, err := c.ClaimNode(ctx, "plain-runner", nil, 30*time.Second, nil)
	if err != nil {
		t.Fatalf("ClaimNode plain: %v", err)
	}
	if n == nil || n.NodeID != "anyone" {
		t.Fatalf("plain runner claim: %+v", n)
	}

	n, err = c.ClaimNode(ctx, "special-runner", []string{"special"}, 30*time.Second, nil)
	if err != nil {
		t.Fatalf("ClaimNode labeled: %v", err)
	}
	if n == nil || n.NodeID != "special" {
		t.Fatalf("special runner claim: %+v", n)
	}
	if n.ClaimedBy != "special-runner" {
		t.Fatalf("claimed_by: %q", n.ClaimedBy)
	}
}

func TestNodeClaimOffer_HTTPUsesEnrolledPriorityAndPreservesPendingState(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now().UTC()
	raw, token, err := st.CreateToken("registered-worker", store.TokenKindRunner, []string{controller.ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnrollExecutor(context.Background(), token.Prefix, store.Executor{
		Name: "desk", Kind: "gateway", Location: "local", BasePriority: 40, PriorityCeiling: 75,
		MaxConcurrent: 1, Principal: token.Principal, Budget: store.ExecutorResource{Cores: 4},
	}); err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: token.Principal, TokenPrefix: token.Prefix}
	if err := st.HeartbeatExecutor(context.Background(), claimant, "desk", store.ExecutorResource{Cores: 4}, 0, now); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()
	c := client.NewWithToken(srv.URL, nil, raw)
	ctx := context.Background()
	seedRunNode(t, st, "run-1", "node-a")
	if err := st.MarkNodeReadyWithPriorityCeiling(ctx, "run-1", "node-a", 100); err != nil {
		t.Fatal(err)
	}
	preparation, err := c.PrepareExecutorClaim(ctx, "desk")
	if err != nil || preparation == nil || preparation.Summary.RunID != "run-1" || preparation.Summary.NodeID != "node-a" {
		t.Fatalf("PrepareExecutorClaim = %+v, %v", preparation, err)
	}
	if preparation.Membership.EffectivePriority != 40 || preparation.Membership.WorkerID != "desk" {
		t.Fatalf("trusted membership = %+v", preparation.Membership)
	}
	executor := client.ExecutorClaim{
		ExecutorName: "desk", HolderID: "holder", ReservationID: "reservation",
		ResourceDigest: preparation.Summary.ResourceDigest, Slot: 0, Lease: time.Minute,
	}
	untrusted, err := json.Marshal(map[string]any{
		"executor_name": "desk", "holder_id": "holder", "run_id": "run-1", "node_id": "node-a",
		"reservation_id": "reservation", "resource_digest": preparation.Summary.ResourceDigest,
		"slot": 0, "claim_priority": 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/v1/nodes/claim", bytes.NewReader(untrusted))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("self-reported priority status = %d, want 400", resp.StatusCode)
	}
	result, err := c.OfferExecutorClaim(ctx, executor, preparation.Summary.RunID, preparation.Summary.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pending || result.Node != nil {
		t.Fatalf("first offer = %+v, want pending", result)
	}

	if _, err := st.DB().Exec(`UPDATE nodes SET offer_started_at = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().Add(-6*time.Second).UnixNano(), "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	result, err = c.OfferExecutorClaim(ctx, executor, preparation.Summary.RunID, preparation.Summary.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Node == nil || result.Pending {
		t.Fatalf("ceiling offer = %+v", result)
	}
	if result.Node.ClaimedBy != "holder" || result.Node.ClaimMembershipID == "" || result.Node.ReservationID != "reservation" {
		t.Fatalf("claim response omitted exact fence: %+v", result.Node)
	}
	if result.Node.ExecutorName != "desk" || result.Node.ExecutorKind != "gateway" || result.Node.ExecutorLocation != "local" {
		t.Fatalf("claim response omitted public attribution: %+v", result.Node)
	}
	if result.Node.CoordinatorID != "" || result.Node.ExecutorID != "" || result.Node.RequiredCoordinatorID != "" ||
		result.Node.ClaimWorkerID != "" || result.Node.ClaimExecutorKind != "" || result.Node.ClaimReservationID != "" {
		t.Fatalf("claim response exposed redundant internal identity: %+v", result.Node)
	}
	var executorName, reservation string
	var slot int
	if err := st.DB().QueryRow(`SELECT claim_executor, claim_reservation, claim_slot FROM nodes WHERE run_id = ? AND node_id = ?`,
		"run-1", "node-a").Scan(&executorName, &reservation, &slot); err != nil {
		t.Fatal(err)
	}
	if executorName != "desk" || reservation != "reservation" || slot != 0 {
		t.Fatalf("claim binding = executor %q reservation %q slot %d", executorName, reservation, slot)
	}
}
