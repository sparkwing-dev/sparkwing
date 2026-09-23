package orchestrator_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the step runs until its context ends or the test gives up on it, so
// a pod that ignores a refused renewal fails the bound instead of hanging.
var claimRefusedRelease = make(chan struct{})

type claimRefusedPipe struct{ sparkwing.Base }

func (claimRefusedPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-claimRefusedRelease:
			return nil
		}
	})
	return nil
}

var claimRefusedOnce sync.Once

func registerClaimRefusedPipe() {
	claimRefusedOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("pod-claim-refused",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return claimRefusedPipe{} })
	})
}

// The controller refuses a pod's claim renewal when the team's credits run
// out, the claim was reaped, or the node was cancelled. The pod has to stop
// the step it is running then, or it bills compute until the Job deadline.
func TestRunNodeCommand_StopsWhenTheClaimRenewalIsRefused(t *testing.T) {
	registerClaimRefusedPipe()
	isolateCheckout(t)
	isolateProfiles(t)
	pinRunNodeEnvironment(t)

	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	if err := orchestrator.PathsAt(home).EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	const runID, nodeID = "run-claim-refused", "build"
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "pod-claim-refused", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := st.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	token, _, err := st.CreateToken("pool", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeRunsRead,
		controller.ScopeRunsState, controller.ScopeRunsWrite,
	}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := controller.New(st, quiet).EnableAuthFromStore().Handler()
	refused := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/nodes/"+nodeID+"/heartbeat") {
			select {
			case refused <- struct{}{}:
			default:
			}
			http.Error(w, `{"error":"insufficient credits"}`, http.StatusConflict)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	claimed, err := client.NewWithToken(srv.URL, nil, token).
		ClaimNodeByID(ctx, runID, nodeID, "k8s-job:sw-refused", time.Minute, false)
	if err != nil {
		t.Fatalf("dispatcher claim: %v", err)
	}
	t.Setenv("SPARKWING_AGENT_TOKEN", token)
	t.Setenv("SPARKWING_NODE_CLAIM_HOLDER", claimed.ClaimedBy)
	t.Setenv("SPARKWING_NODE_CLAIM_GENERATION", strconv.FormatInt(claimed.ClaimGeneration, 10))
	t.Setenv("SPARKWING_NODE_CLAIM_MEMBERSHIP", claimed.ClaimMembershipID)
	t.Setenv("SPARKWING_NODE_CLAIM_RESERVATION", claimed.ReservationID)
	t.Setenv("SPARKWING_NODE_CLAIM_LEASE_SECONDS", "600")

	done := make(chan error, 1)
	go func() { done <- orchestrator.RunNodeCommand(runNodeArgs(srv.URL, runID, nodeID)) }()

	// safety: the bound is two renewal periods past the first refusal, which a
	// pod that stops on the refusal meets and one that logs and carries on
	// never does.
	select {
	case <-refused:
	case err := <-done:
		t.Fatalf("RunNodeCommand returned %v before any renewal was refused", err)
	case <-time.After(4 * store.DispatchedHeartbeatInterval):
		close(claimRefusedRelease)
		t.Fatal("the pod never renewed its claim")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("RunNodeCommand succeeded after its claim renewal was refused, want a failure so the pod exits non-zero")
		}
	case <-time.After(2 * store.DispatchedHeartbeatInterval):
		close(claimRefusedRelease)
		<-done
		t.Fatal("the node kept running after the controller refused its claim renewal")
	}
}
