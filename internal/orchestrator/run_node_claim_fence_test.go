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

type dispatchedClaimPipe struct{ sparkwing.Base }

func (dispatchedClaimPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", func(context.Context) error { return nil })
	return nil
}

var dispatchedClaimOnce sync.Once

func registerDispatchedClaimPipe() {
	dispatchedClaimOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("pod-dispatched-claim",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return dispatchedClaimPipe{} })
	})
}

// safety: the recorder answers the fence question the live defect asked, which
// is whether the pod's own writes carry the claim the dispatcher took for it.
type fenceRecorder struct {
	mu    sync.Mutex
	seen  map[string]string
	plain []string
}

func (f *fenceRecorder) record(r *http.Request) {
	if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/nodes/") ||
		strings.HasSuffix(r.URL.Path, "/claim") {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if holder := r.Header.Get(store.ClaimHolderHeader); holder != "" {
		f.seen[r.URL.Path] = holder + "/" + r.Header.Get(store.ClaimGenerationHeader)
		return
	}
	f.plain = append(f.plain, r.URL.Path)
}

// The warm fallback Job runs a node no agent claimed, so the pod is the only
// holder of that claim and every state write it makes has to prove it.
func TestRunNodeCommand_SendsTheDispatchedClaimFence(t *testing.T) {
	registerDispatchedClaimPipe()
	isolateCheckout(t)
	isolateProfiles(t)

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
	const runID, nodeID = "run-dispatched-claim", "build"
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "pod-dispatched-claim", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	token, _, err := st.CreateToken("pool", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeRunsRead,
		controller.ScopeRunsState, controller.ScopeRunsWrite,
	}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	recorder := &fenceRecorder{seen: map[string]string{}}
	handler := controller.New(st, quiet).EnableAuthFromStore().Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	claimed, err := client.NewWithToken(srv.URL, nil, token).
		ClaimNodeByID(ctx, runID, nodeID, "k8s-job:sw-1", time.Minute)
	if err != nil {
		t.Fatalf("dispatcher claim: %v", err)
	}
	t.Setenv("SPARKWING_AGENT_TOKEN", token)
	t.Setenv("SPARKWING_NODE_CLAIM_HOLDER", claimed.ClaimedBy)
	t.Setenv("SPARKWING_NODE_CLAIM_GENERATION", strconv.FormatInt(claimed.ClaimGeneration, 10))
	t.Setenv("SPARKWING_NODE_CLAIM_MEMBERSHIP", claimed.ClaimMembershipID)
	t.Setenv("SPARKWING_NODE_CLAIM_RESERVATION", claimed.ReservationID)
	t.Setenv("SPARKWING_NODE_CLAIM_LEASE_SECONDS", "600")

	if err := orchestrator.RunNodeCommand([]string{"--controller", srv.URL, runID, nodeID}); err != nil {
		t.Fatalf("RunNodeCommand: %v", err)
	}

	n, err := st.GetNode(ctx, runID, nodeID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Status != "done" || n.Outcome != string(sparkwing.Success) {
		t.Fatalf("node = status %q outcome %q, want the pod's terminal state", n.Status, n.Outcome)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.plain) > 0 {
		t.Fatalf("these node writes carried no claim fence: %v", recorder.plain)
	}
	want := claimed.ClaimedBy + "/" + strconv.FormatInt(claimed.ClaimGeneration, 10)
	if len(recorder.seen) == 0 {
		t.Fatal("the pod made no fenced node write")
	}
	for path, got := range recorder.seen {
		if got != want {
			t.Errorf("%s carried fence %q, want %q", path, got, want)
		}
	}
}

// A pod the dispatcher handed no claim keeps running unfenced, which is the
// local invocation and the older controller that cannot award one.
func TestRunNodeCommand_RunsUnfencedWithoutADispatchedClaim(t *testing.T) {
	registerDispatchedClaimPipe()
	isolateCheckout(t)
	isolateProfiles(t)

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
	const runID, nodeID = "run-unfenced", "build"
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "pod-dispatched-claim", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).Handler())
	defer srv.Close()

	if err := orchestrator.RunNodeCommand([]string{"--controller", srv.URL, runID, nodeID}); err != nil {
		t.Fatalf("RunNodeCommand: %v", err)
	}
	n, err := st.GetNode(ctx, runID, nodeID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Status != "done" || n.Outcome != string(sparkwing.Success) {
		t.Fatalf("node = status %q outcome %q, want success", n.Status, n.Outcome)
	}
}
