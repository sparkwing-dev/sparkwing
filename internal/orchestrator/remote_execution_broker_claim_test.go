package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// D-SEC-08 end to end: a brokered node running on its claim token reads a
// pipeline reference and starts, polls and finds its own child run through
// the broker against an authenticating controller.
func TestRemoteExecutionBrokerCarriesCrossPipelineCallsOnTheClaimToken(t *testing.T) {
	st, err := teststore.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now()
	runner, _, err := st.CreateToken("pool", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeRunsState, controller.ScopeLogsWrite,
	}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "lib-1", Pipeline: "lib", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "lib-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(ctx, "lib-1", "build", "success", "", []byte(`{"version":"1.2.3"}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, "lib-1", "success", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "parent", Pipeline: "deploy", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: "parent", Pipeline: "deploy", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "parent", NodeID: "ship", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "parent", "ship"); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).EnableAuthFromStore().Handler())
	t.Cleanup(upstream.Close)
	n, err := client.NewWithToken(upstream.URL, nil, runner).ClaimNode(ctx, "pool:1", nil, time.Minute, nil)
	if err != nil || n == nil || n.ClaimToken == "" {
		t.Fatalf("claim = %+v, %v", n, err)
	}
	fence := store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}
	broker, err := startRemoteExecutionBroker(upstream.URL, "", n.ClaimToken, "parent", "ship", fence, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	node := client.NewWithToken(broker.URL(), nil, broker.capability)

	ref, err := node.GetLatestRun(ctx, "lib", []string{"success"}, 0)
	if err != nil || ref.ID != "lib-1" {
		t.Fatalf("latest lib run through the broker = %+v, %v", ref, err)
	}
	if out, err := node.GetNodeOutput(ctx, ref.ID, "build"); err != nil || !strings.Contains(string(out), "1.2.3") {
		t.Fatalf("lib-1's output through the broker = %s, %v", out, err)
	}
	child, err := node.CreateTrigger(ctx, client.TriggerRequest{
		Pipeline: "lib", Trigger: client.TriggerMeta{Source: "await-pipeline"},
		ParentRunID: "parent", ParentNodeID: "ship",
	})
	if err != nil || child.RunID == "" {
		t.Fatalf("a child run through the broker = %+v, %v", child, err)
	}
	if run, err := node.GetRun(ctx, child.RunID); err != nil || run.ID != child.RunID {
		t.Fatalf("polling the child through the broker = %+v, %v", run, err)
	}
	if id, err := node.FindSpawnedChildTriggerID(ctx, "parent", "ship", "lib"); err != nil || id != child.RunID {
		t.Fatalf("the spawned child through the broker = %q, %v", id, err)
	}
	if _, err := node.GetRun(ctx, "lib-1"); err == nil {
		t.Fatal("the broker and the claim let a node read a run it did not start")
	}
}
