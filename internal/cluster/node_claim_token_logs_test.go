package cluster

import (
	"context"
	"net/http"
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

// A node's process writes its log with its claim token: the logs service has
// the controller check the write against the node's live claim and files it
// under that claim's attempt, and the claim's end stops the next write.
func TestNodeClaimToken_WritesTheNodesLogUntilTheClaimEnds(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	runner, _, err := st.CreateToken("pool", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeRunsState, controller.ScopeLogsWrite,
	}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctrlSrv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(ctrlSrv.Close)
	logSrv, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var bearers []string
	logHandler := logSrv.WithControllerAuth(ctrlSrv.URL, 0).Handler()
	logsHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearers = append(bearers, r.Header.Get("Authorization"))
		logHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(logsHTTP.Close)
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "r1", "n1"); err != nil {
		t.Fatal(err)
	}
	n, err := client.NewWithToken(ctrlSrv.URL, nil, runner).ClaimNode(ctx, "pool:1", nil, time.Minute, nil)
	if err != nil || n == nil || n.ClaimToken == "" {
		t.Fatalf("claim node: %+v, %v", n, err)
	}
	fence := store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}
	node := client.NewWithToken(ctrlSrv.URL, nil, n.ClaimToken)
	attemptCtx := store.WithExecutionAttemptOrdinal(store.WithNodeClaimFence(ctx, fence), 1)
	if err := node.AcknowledgeNodeExecutionStart(attemptCtx, "r1", "n1", store.ExecutionStart{
		HolderID: fence.HolderID, ClaimGeneration: fence.ClaimGeneration, AttemptOrdinal: 1,
	}); err != nil {
		t.Fatalf("execution start with the claim token: %v", err)
	}
	logc := logs.NewClientWithToken(logsHTTP.URL, nil, n.ClaimToken)
	if err := logc.Append(attemptCtx, "r1", "n1", []byte("{\"msg\":\"step 1\"}\n")); err != nil {
		t.Fatalf("append with the claim token: %v", err)
	}
	if err := node.FinishNode(attemptCtx, "r1", "n1", "success", "", nil); err != nil {
		t.Fatalf("finish with the claim token: %v", err)
	}
	if err := logc.Append(attemptCtx, "r1", "n1", []byte("{\"msg\":\"late\"}\n")); err == nil {
		t.Fatal("an append after the claim ended was accepted")
	}
	for _, b := range bearers {
		if strings.Contains(b, runner) {
			t.Fatal("a log write carried the runner's bearer")
		}
	}
}
