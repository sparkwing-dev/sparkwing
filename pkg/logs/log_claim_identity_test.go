package logs_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var logClaimScopes = []string{
	controller.ScopeNodesClaim, controller.ScopeTriggersClaim, controller.ScopeTriggersRead,
	controller.ScopeRunsState, controller.ScopeRunsRead, controller.ScopeLogsRead, controller.ScopeLogsWrite,
}

type logClaimFixture struct {
	store      *store.Store
	controller *client.Client
	logs       *logs.Client
	controlURL string
	logsURL    string
}

func newLogClaimFixture(t *testing.T, principal string) (*logClaimFixture, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	raw, _, err := st.CreateToken(principal, store.TokenKindRunner, logClaimScopes, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	controllerServer := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(controllerServer.Close)
	logServer, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	logsHTTP := httptest.NewServer(logServer.WithControllerAuth(controllerServer.URL, 0).Handler())
	t.Cleanup(logsHTTP.Close)
	return &logClaimFixture{
		store:      st,
		controller: client.NewWithToken(controllerServer.URL, nil, raw),
		logs:       logs.NewClientWithToken(logsHTTP.URL, nil, raw),
		controlURL: controllerServer.URL,
		logsURL:    logsHTTP.URL,
	}, raw
}

// safety: warm mode marks the node ready under the trigger claim and a pool
// runner takes it through the legacy claim route, which is the pair that put
// the node's closing lines on the far side of its execution attempt.
func warmReadyNode(t *testing.T, f *logClaimFixture, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := f.store.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: "demo", Repo: "acme/web", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := f.controller.ClaimTrigger(ctx)
	if err != nil || trigger == nil {
		t.Fatalf("ClaimTrigger = (%+v, %v)", trigger, err)
	}
	triggerCtx := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: trigger.ClaimSeq})
	if err := f.controller.CreateRun(triggerCtx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.controller.CreateNode(triggerCtx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.controller.MarkNodeReady(triggerCtx, runID, nodeID); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
}

func TestLogs_ClaimedRunnerAppendsItsClosingLinesAfterTheAttemptCloses(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f, _ := newLogClaimFixture(t, "runner")
	ctx := context.Background()
	warmReadyNode(t, f, "run-warm", "build")

	node, err := f.controller.ClaimNode(ctx, "runner:box:1", nil, time.Minute, nil)
	if err != nil || node == nil {
		t.Fatalf("ClaimNode = (%+v, %v)", node, err)
	}
	fence := store.NodeClaimFence{
		HolderID: node.ClaimedBy, MembershipID: node.ClaimMembershipID,
		ReservationID: node.ReservationID, ClaimGeneration: node.ClaimGeneration,
	}
	claimCtx := store.WithNodeClaimFence(ctx, fence)
	if err := f.controller.StartNode(claimCtx, "run-warm", "build"); err != nil {
		t.Fatalf("StartNode: %v", err)
	}
	if err := f.controller.AcknowledgeNodeExecutionStart(claimCtx, "run-warm", "build", store.ExecutionStart{
		HolderID: fence.HolderID, MembershipID: fence.MembershipID, ReservationID: fence.ReservationID,
		ClaimGeneration: fence.ClaimGeneration, AttemptOrdinal: 1,
	}); err != nil {
		t.Fatalf("AcknowledgeNodeExecutionStart: %v", err)
	}
	appendCtx := store.WithExecutionAttemptOrdinal(claimCtx, 1)
	if err := f.logs.Append(appendCtx, "run-warm", "build", []byte("work\n")); err != nil {
		t.Fatalf("append during the attempt: %v", err)
	}
	if err := f.controller.FinishNodeExecutionAttempt(claimCtx, "run-warm", "build", store.ExecutionAttemptFinish{
		HolderID: fence.HolderID, MembershipID: fence.MembershipID, ReservationID: fence.ReservationID,
		ClaimGeneration: fence.ClaimGeneration, AttemptOrdinal: 1, Outcome: "success",
	}); err != nil {
		t.Fatalf("FinishNodeExecutionAttempt: %v", err)
	}
	if err := f.logs.Append(appendCtx, "run-warm", "build", []byte("node_end\n")); err != nil {
		t.Fatalf("append after the attempt closed: %v", err)
	}
	body, err := f.logs.Read(ctx, "run-warm", "build")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "work\n") || !strings.Contains(string(body), "node_end\n") {
		t.Fatalf("stored log = %q, want both the work and the closing line", body)
	}
}

func TestLogs_TriggerRunnerAppendsItsClosingLinesAfterTheAttemptCloses(t *testing.T) {
	f, _ := newLogClaimFixture(t, "trigger-runner")
	ctx := context.Background()
	if err := f.store.CreateTrigger(ctx, store.Trigger{
		ID: "run-inline", Pipeline: "demo", Repo: "acme/web", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := f.controller.ClaimTrigger(ctx)
	if err != nil || trigger == nil {
		t.Fatalf("ClaimTrigger = (%+v, %v)", trigger, err)
	}
	triggerCtx := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: trigger.ClaimSeq})
	if err := f.controller.CreateRun(triggerCtx, store.Run{
		ID: "run-inline", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.controller.CreateNode(triggerCtx, store.Node{
		RunID: "run-inline", NodeID: "build", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.controller.StartNode(triggerCtx, "run-inline", "build"); err != nil {
		t.Fatal(err)
	}
	if err := f.controller.AcknowledgeNodeExecutionStart(triggerCtx, "run-inline", "build", store.ExecutionStart{
		ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1,
	}); err != nil {
		t.Fatalf("AcknowledgeNodeExecutionStart: %v", err)
	}
	appendCtx := store.WithExecutionAttemptOrdinal(triggerCtx, 1)
	if err := f.logs.Append(appendCtx, "run-inline", "build", []byte("work\n")); err != nil {
		t.Fatalf("append during the attempt: %v", err)
	}
	if err := f.controller.FinishNodeExecutionAttempt(triggerCtx, "run-inline", "build", store.ExecutionAttemptFinish{
		ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1, Outcome: "success",
	}); err != nil {
		t.Fatalf("FinishNodeExecutionAttempt: %v", err)
	}
	if err := f.logs.Append(appendCtx, "run-inline", "build", []byte("node_end\n")); err != nil {
		t.Fatalf("append after the attempt closed: %v", err)
	}
	body, err := f.logs.Read(ctx, "run-inline", "build")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "work\n") || !strings.Contains(string(body), "node_end\n") {
		t.Fatalf("stored log = %q, want both the work and the closing line", body)
	}
}

func TestLogs_AppendFromAPrincipalWithoutTheNodeClaimIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f, _ := newLogClaimFixture(t, "runner")
	ctx := context.Background()
	warmReadyNode(t, f, "run-intruder", "build")

	node, err := f.controller.ClaimNode(ctx, "runner:box:1", nil, time.Minute, nil)
	if err != nil || node == nil {
		t.Fatalf("ClaimNode = (%+v, %v)", node, err)
	}
	fence := store.NodeClaimFence{
		HolderID: node.ClaimedBy, MembershipID: node.ClaimMembershipID,
		ReservationID: node.ReservationID, ClaimGeneration: node.ClaimGeneration,
	}
	claimCtx := store.WithNodeClaimFence(ctx, fence)
	if err := f.controller.AcknowledgeNodeExecutionStart(claimCtx, "run-intruder", "build", store.ExecutionStart{
		HolderID: fence.HolderID, MembershipID: fence.MembershipID, ReservationID: fence.ReservationID,
		ClaimGeneration: fence.ClaimGeneration, AttemptOrdinal: 1,
	}); err != nil {
		t.Fatalf("AcknowledgeNodeExecutionStart: %v", err)
	}
	intruderRaw, _, err := f.store.CreateToken("intruder", store.TokenKindRunner, logClaimScopes, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	intruder := logs.NewClientWithToken(f.logsURL, nil, intruderRaw)
	appendCtx := store.WithExecutionAttemptOrdinal(claimCtx, 1)
	if err := intruder.Append(appendCtx, "run-intruder", "build", []byte("forged\n")); !errors.Is(err, logs.ErrClaimConflict) {
		t.Fatalf("append by a principal without the claim = %v, want ErrClaimConflict", err)
	}
	if _, err := f.store.DB().ExecContext(ctx,
		`UPDATE nodes SET lease_expires_at = ? WHERE run_id = 'run-intruder' AND node_id = 'build'`,
		time.Now().Add(-time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := f.logs.Append(appendCtx, "run-intruder", "build", []byte("expired\n")); !errors.Is(err, logs.ErrClaimConflict) {
		t.Fatalf("append after the claim lease lapsed = %v, want ErrClaimConflict", err)
	}
}
