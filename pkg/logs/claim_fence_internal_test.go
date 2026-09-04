package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestAttemptSubstreamCannotContaminateReplacementAfterValidation(t *testing.T) {
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
	first := seedHistoricalAssistedLogTestClaim(t, st, identity, "source", "build", "agent:a", "reservation-a")
	if err := st.AcknowledgeNodeExecutionStart(ctx, first.RunID, first.NodeID, identity, store.ExecutionStart{
		HolderID: first.ClaimedBy, MembershipID: first.ClaimMembershipID, ReservationID: first.ReservationID,
		ClaimGeneration: first.ClaimGeneration, AttemptOrdinal: 1,
	}); err != nil {
		t.Fatal(err)
	}
	firstCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: identity, HolderID: first.ClaimedBy, MembershipID: first.ClaimMembershipID,
		ReservationID: first.ReservationID, ClaimGeneration: first.ClaimGeneration,
	})
	firstCtx = store.WithExecutionAttemptOrdinal(firstCtx, 1)

	controllerServer := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer controllerServer.Close()
	root := t.TempDir()
	server, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	validated := make(chan struct{})
	resume := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})
	var once sync.Once
	server.diskSpace = func(string) (uint64, uint64, bool) {
		once.Do(func() {
			close(validated)
			<-resume
		})
		return 1 << 40, 1 << 40, true
	}
	logsHTTP := httptest.NewServer(server.WithControllerAuth(controllerServer.URL, 0).Handler())
	defer logsHTTP.Close()
	logClient := NewClientWithToken(logsHTTP.URL, nil, raw)
	appendDone := make(chan error, 1)
	go func() { appendDone <- logClient.Append(firstCtx, first.RunID, first.NodeID, []byte("late-a\n")) }()
	<-validated

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
	second := seedHistoricalAssistedLogTestClaim(t, st, identity, retryID, "build", "agent:b", "reservation-b")
	if err := st.AcknowledgeNodeExecutionStart(ctx, second.RunID, second.NodeID, identity, store.ExecutionStart{
		HolderID: second.ClaimedBy, MembershipID: second.ClaimMembershipID, ReservationID: second.ReservationID,
		ClaimGeneration: second.ClaimGeneration, AttemptOrdinal: 2,
	}); err != nil {
		t.Fatal(err)
	}
	secondCtx := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		Claimant: identity, HolderID: second.ClaimedBy, MembershipID: second.ClaimMembershipID,
		ReservationID: second.ReservationID, ClaimGeneration: second.ClaimGeneration,
	})
	secondCtx = store.WithExecutionAttemptOrdinal(secondCtx, 2)
	close(resume)
	if err := <-appendDone; err != nil {
		t.Fatalf("already validated append: %v", err)
	}
	if err := logClient.Append(secondCtx, retryID, "build", []byte("from-b\n")); err != nil {
		t.Fatal(err)
	}

	firstPath := filepath.Join(root, "runs", nodeAttemptPath(first.RunID, first.NodeID, first.ClaimGeneration, 1))
	firstBytes, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != "late-a\n" {
		t.Fatalf("first attempt substream = %q", firstBytes)
	}
	secondBytes, err := logClient.Read(ctx, retryID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if string(secondBytes) != "from-b\n" {
		t.Fatalf("replacement log = %q, want no first-attempt bytes", secondBytes)
	}
}

func seedHistoricalAssistedLogTestClaim(t *testing.T, st *store.Store, identity store.ClaimIdentity, runID, nodeID, holder, reservation string) *store.Node {
	t.Helper()
	coordinatorID, err := st.CoordinatorID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// safety: schema 31 refuses unsealed assisted offers; this fixture restores
	// only the historical attribution needed to exercise attempt-log fencing.
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

func TestTriggerGenerationSubstreamCannotContaminateReplacementAfterValidation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw, token, err := st.CreateToken("runner", store.TokenKindRunner,
		[]string{controller.ScopeTriggersClaim, controller.ScopeLogsRead, controller.ScopeLogsWrite}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: token.Principal, TokenPrefix: token.Prefix}
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "run", Pipeline: "demo", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	first, err := st.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run", NodeID: "build", Status: "running"}); err != nil {
		t.Fatal(err)
	}

	controllerServer := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer controllerServer.Close()
	root := t.TempDir()
	server, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	validated := make(chan struct{})
	resume := make(chan struct{})
	var secondOnce sync.Once
	server.diskSpace = func(string) (uint64, uint64, bool) {
		secondOnce.Do(func() {
			close(validated)
			<-resume
		})
		return 1 << 40, 1 << 40, true
	}
	logsHTTP := httptest.NewServer(server.WithControllerAuth(controllerServer.URL, 0).Handler())
	defer logsHTTP.Close()
	logClient := NewClientWithToken(logsHTTP.URL, nil, raw)
	firstCtx := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: first.ClaimSeq})
	appendDone := make(chan error, 1)
	go func() { appendDone <- logClient.Append(firstCtx, "run", "_compile", []byte("late-first\n")) }()
	<-validated

	if _, err := st.DB().ExecContext(ctx, `UPDATE triggers
 SET claim_seq = claim_seq + 1, lease_expires_at = ? WHERE id = 'run'`, time.Now().Add(time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-appendDone; err != nil {
		t.Fatalf("already validated first append: %v", err)
	}
	secondCtx := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: first.ClaimSeq + 1})
	if err := logClient.Append(secondCtx, "run", "_compile", []byte("second\n")); err != nil {
		t.Fatal(err)
	}

	for generation, want := range map[int64]string{first.ClaimSeq: "late-first\n", first.ClaimSeq + 1: "second\n"} {
		req, err := http.NewRequest(http.MethodGet, logsHTTP.URL+"/api/v1/logs/run/_compile?trigger_generation="+strconv.FormatInt(generation, 10), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+raw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != http.StatusOK || string(body) != want {
			t.Fatalf("generation %d response = %d %q, want 200 %q", generation, resp.StatusCode, body, want)
		}
	}
}

func TestAppendRejectsMixedTriggerAndNodeAttemptIdentity(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw, token, err := st.CreateToken("runner", store.TokenKindRunner,
		[]string{controller.ScopeTriggersClaim, controller.ScopeLogsWrite}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: token.Principal, TokenPrefix: token.Prefix}
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "run", Pipeline: "demo", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run", NodeID: "build", Status: "running"}); err != nil {
		t.Fatal(err)
	}

	controllerServer := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer controllerServer.Close()
	root := t.TempDir()
	server, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	logsHTTP := httptest.NewServer(server.WithControllerAuth(controllerServer.URL, 0).Handler())
	defer logsHTTP.Close()
	req, err := http.NewRequest(http.MethodPost, logsHTTP.URL+"/api/v1/logs/run/build", bytes.NewReader([]byte("forged\n")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set(store.TriggerGenerationHeader, strconv.FormatInt(claimed.ClaimSeq, 10))
	req.Header.Set(store.ClaimHolderHeader, "forged-holder")
	req.Header.Set(store.ClaimGenerationHeader, "99")
	req.Header.Set(store.AttemptOrdinalHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mixed identity status = %d, want 400", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "runs", nodeAttemptPath("run", "build", 99, 1))); !os.IsNotExist(err) {
		t.Fatalf("forged attempt stream exists: %v", err)
	}
}
