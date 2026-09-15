package controller_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func boundaryBoundClient(t *testing.T) (*store.Store, *client.Client, store.ClaimIdentity) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := store.WithCreatingPrincipal(context.Background(), "tenant-a")
	if err := st.CreateRun(ctx, store.Run{
		ID: "bound-run", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "bound-run", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	_, pool, err := st.CreateTokenWith(ctx, "cloud-pool", store.TokenKindRunner,
		[]string{
			controller.ScopeNodesClaim, controller.ScopeTriggersClaim, controller.ScopeRunsState,
			controller.ScopeSecretsRead, controller.ScopeLogsWrite,
		},
		0, time.Now(), store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCredit, "review", "admin"); err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: pool.Principal, TokenPrefix: pool.Prefix}
	result, err := st.FinalizeExecutorClaimRound(ctx, "bound-run", "build", store.DispatchHosted,
		&store.HostedClaimSpec{
			Binding: store.ExecutionCredentialBinding{
				RunID: "bound-run", RootNodeID: "build",
				DelegatedPrincipal: pool.Principal, DelegatedTokenPrefix: pool.Prefix,
			},
			Scopes: []string{
				controller.ScopeNodesClaim, controller.ScopeTriggersClaim,
				controller.ScopeRunsState, controller.ScopeSecretsRead, controller.ScopeLogsWrite,
				controller.ScopeAdmin,
			},
			TTL: time.Hour, Lifetime: time.Hour,
		})
	if err != nil {
		t.Fatal(err)
	}
	ctrl := controller.New(st, nil).EnableAuthFromStore()
	srv := httptest.NewServer(ctrl.Handler())
	t.Cleanup(func() {
		srv.Close()
		_ = ctrl.Shutdown(context.Background())
	})
	return st, client.NewWithToken(srv.URL, srv.Client(), result.Hosted.RawBearer), claimant
}

func TestBoundCredentialCannotAppendToAnotherClaim(t *testing.T) {
	st, bound, claimant := boundaryBoundClient(t)
	ctx := store.WithCreatingPrincipal(context.Background(), "tenant-b")
	if err := st.CreateRun(ctx, store.Run{
		ID: "other-run", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "other-run", NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "other-run", "work"); err != nil {
		t.Fatal(err)
	}
	node, err := st.ClaimNextReadyNode(ctx, claimant, "other-holder", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	fence := store.NodeClaimFence{
		Claimant: claimant, HolderID: node.ClaimedBy,
		MembershipID: node.ClaimMembershipID, ReservationID: node.ReservationID,
		ClaimGeneration: node.ClaimGeneration,
	}
	if err := bound.AppendEvent(store.WithNodeClaimFence(context.Background(), fence),
		"other-run", "work", "review", nil); err == nil {
		t.Fatal("bound credential appended an event to another run's claim")
	}
}

func TestBoundCredentialAdminScopeCannotBypassBoundary(t *testing.T) {
	_, bound, _ := boundaryBoundClient(t)
	if _, err := bound.ListRuns(context.Background(), store.RunFilter{}); err == nil {
		t.Fatal("bound credential listed global runs through admin scope")
	}
	if _, err := bound.GetPipelineProfile(context.Background(), "other-pipeline", "build"); err == nil {
		t.Fatal("bound credential read another pipeline profile through admin scope")
	}
}

func TestBoundCredentialCreatesAwaitFromAttributedDynamicChild(t *testing.T) {
	st, bound, _ := boundaryBoundClient(t)
	ctx := context.Background()
	root, err := st.GetNode(ctx, "bound-run", "build")
	if err != nil {
		t.Fatal(err)
	}
	fenced := store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: root.ClaimedBy, MembershipID: root.ClaimMembershipID,
		ReservationID: root.ReservationID, ClaimGeneration: root.ClaimGeneration,
	})
	if err := bound.CreateNode(fenced, store.Node{
		RunID: "bound-run", NodeID: "build/dynamic", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	childID, err := bound.EnqueueTriggerForAwait(fenced,
		"child", nil, "bound-run", "build/dynamic", "result", "",
		"await-pipeline", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	trigger, err := st.GetTrigger(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.ParentNodeID != "build/dynamic" || trigger.RequestedOutputNodeID != "result" {
		t.Fatalf("child grant = parent %q output %q", trigger.ParentNodeID, trigger.RequestedOutputNodeID)
	}

	if err := st.CreateNode(ctx, store.Node{
		RunID: "bound-run", NodeID: "build/static", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := bound.EnqueueTriggerForAwait(fenced,
		"child-static", nil, "bound-run", "build/static", "result", "",
		"await-pipeline", "", "", "", nil); err == nil {
		t.Fatal("bound credential created an await from an unattributed prefix node")
	}
}

func TestBoundCredentialCannotAppendToSiblingClaimInItsRun(t *testing.T) {
	st, bound, claimant := boundaryBoundClient(t)
	ctx := context.Background()
	if err := st.CreateNode(ctx, store.Node{RunID: "bound-run", NodeID: "sibling", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "bound-run", "sibling"); err != nil {
		t.Fatal(err)
	}
	node, err := st.ClaimNextReadyNode(ctx, claimant, "sibling-holder", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	fence := store.NodeClaimFence{
		Claimant: claimant, HolderID: node.ClaimedBy,
		MembershipID: node.ClaimMembershipID, ReservationID: node.ReservationID,
		ClaimGeneration: node.ClaimGeneration,
	}
	if err := bound.AppendEvent(store.WithNodeClaimFence(ctx, fence),
		"bound-run", "sibling", "review", nil); err == nil {
		t.Fatal("bound credential appended an event through a sibling claim")
	}
}

func TestBoundCredentialReadsOnlyRequestedDirectChildOutput(t *testing.T) {
	st, bound, _ := boundaryBoundClient(t)
	ctx := store.WithCreatingPrincipal(context.Background(), "tenant-a")
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "child-run", Pipeline: "child", Status: "pending", CreatedAt: time.Now(),
		ParentRunID: "bound-run", ParentNodeID: "build",
		RequestedOutputNodeID: "result",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: "child-run", Pipeline: "child", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{"result", "private"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "child-run", NodeID: nodeID, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishNode(ctx, "child-run", nodeID, "success", "", []byte(`{"value":"`+nodeID+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if output, err := bound.GetNodeOutput(context.Background(), "child-run", "result"); err != nil || string(output) != `{"value":"result"}` {
		t.Fatalf("requested child output = %s, %v", output, err)
	}
	if _, err := bound.GetRunForExecution(context.Background(), "child-run"); err != nil {
		t.Fatalf("read direct child status: %v", err)
	}
	if _, err := bound.GetNode(context.Background(), "child-run", "result"); err == nil {
		t.Fatal("bound credential read metadata for its requested child output")
	}
	if _, err := bound.ListNodes(context.Background(), "child-run"); err == nil {
		t.Fatal("bound credential listed its direct child's nodes")
	}
	if _, err := bound.GetNode(context.Background(), "child-run", "private"); err == nil {
		t.Fatal("bound credential read arbitrary node metadata from its direct child run")
	}
	if _, err := bound.GetNodeOutput(context.Background(), "child-run", "private"); err == nil {
		t.Fatal("bound credential read an unrequested output from its direct child run")
	}
}
