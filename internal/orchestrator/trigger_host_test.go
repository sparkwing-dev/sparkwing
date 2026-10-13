//go:build unix

package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type hostedTrigger struct {
	st    *store.Store
	url   string
	token string
	bin   string
	trig  *store.Trigger
}

// safety: a runner token holds only what a trigger-claiming runner holds, so
// a route the host reaches for beyond it fails the test.
func newHostedTrigger(t *testing.T, args map[string]string) *hostedTrigger {
	t.Helper()
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	h := &hostedTrigger{bin: podFixture(t)}
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.st = st
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	h.url = srv.URL
	h.token, _, err = st.CreateToken("trigger-runner", store.TokenKindRunner, []string{
		controller.ScopeTriggersClaim, controller.ScopeTriggersRead, controller.ScopeRunsState,
		controller.ScopeRunsRead, controller.ScopeNodesClaim, controller.ScopeLogsRead,
	}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	ctx, now := store.WithoutCreditMetering(t.Context()), time.Now()
	if err := st.CreateTriggerWithRun(ctx, store.Trigger{
		ID: "run-hosted", Pipeline: "podnode", Args: args, Repo: "acme/widgets",
		GitBranch: "main", GitSHA: strings.Repeat("b", 40), CreatedAt: now,
	}, store.Run{ID: "run-hosted", Pipeline: "podnode", Status: "pending", Args: args, CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	h.trig, err = client.NewWithToken(h.url, nil, h.token).ClaimTriggerFor(t.Context(), nil, nil)
	if err != nil || h.trig == nil {
		t.Fatalf("claim the trigger: %+v %v", h.trig, err)
	}
	return h
}

func (h *hostedTrigger) host(t *testing.T, ctx context.Context, r runner.Runner, heartbeat time.Duration) error {
	t.Helper()
	return HostTriggerRun(ctx, TriggerHost{
		ControllerURL: h.url, Token: h.token, Trigger: h.trig, Binary: h.bin, Dir: t.TempDir(),
		Runner: r, Heartbeat: heartbeat, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func (h *hostedTrigger) run(t *testing.T) *store.Run {
	t.Helper()
	run, err := h.st.GetRun(t.Context(), h.trig.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func (h *hostedTrigger) node(t *testing.T, id string) *store.Node {
	t.Helper()
	n, err := h.st.GetNode(t.Context(), h.trig.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A runner hosts a claimed trigger's run itself: it plans in the pipeline's
// own process, runs each node in a process of its own under a claim it takes
// for that node, and finishes the trigger. Neither the runner's token nor a
// claim token reaches the node.
func TestTriggerHost_HostsTheRunWithEachNodeUnderItsOwnClaim(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "env")
	h := newHostedTrigger(t, map[string]string{"envout": envOut})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := h.host(t, ctx, nil, 0); err != nil {
		t.Fatalf("host: %v", err)
	}
	if run := h.run(t); run.Status != "success" || len(run.PlanSnapshot) == 0 {
		t.Fatalf("run = %s (%s), snapshot %d bytes; want success with the accepted plan", run.Status, run.Error, len(run.PlanSnapshot))
	}
	n := h.node(t, "build")
	if n.Status != "done" || n.Outcome != string(sparkwing.Success) || !strings.HasPrefix(n.ClaimedBy, claimedNodeHolder) {
		t.Fatalf("build = %s/%s (%s) claimed by %q, want done/success under the host's node claim", n.Status, n.Outcome, n.Error, n.ClaimedBy)
	}
	env, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), h.token) || strings.Contains(string(env), store.ClaimTokenPrefix+"_") {
		t.Fatalf("the node's environment holds a controller credential:\n%s", env)
	}
	trig, err := h.st.GetTrigger(t.Context(), h.trig.ID)
	if err != nil || trig.Status != "done" {
		t.Fatalf("trigger = %+v %v, want done", trig, err)
	}
}

// safety: a node is checked against the plan the run host accepted, so a
// pipeline whose plan differs between its processes fails the node with
// plan_drift before the node's body runs.
func TestTriggerHost_ANodeThatPlansDifferentlyFailsWithPlanDrift(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "env")
	h := newHostedTrigger(t, map[string]string{"envout": envOut, "drift": filepath.Join(t.TempDir(), "plans")})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := h.host(t, ctx, nil, 0); err != nil {
		t.Fatalf("host: %v", err)
	}
	if run := h.run(t); run.Status != "failed" {
		t.Fatalf("run = %s, want failed", run.Status)
	}
	if n := h.node(t, "build"); n.Outcome != string(sparkwing.Failed) || n.FailureReason != "plan_drift" {
		t.Fatalf("build = %s (%s, %s), want failed for plan drift", n.Outcome, n.FailureReason, n.Error)
	}
	if _, err := os.Stat(envOut); !os.IsNotExist(err) {
		t.Fatalf("the drifted node's body ran: %v", err)
	}
}

// safety: the controller hands a node to one holder, so a node another host
// already claimed is neither run nor finished by this one, and its row stays
// with the holder that has it.
func TestClaimedNodeHost_RefusesANodeAnotherHostHolds(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "env")
	h := newHostedTrigger(t, map[string]string{"envout": envOut})
	ctx := store.WithoutCreditMetering(t.Context())
	if err := h.st.CreateNode(ctx, store.Node{RunID: h.trig.ID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	c := client.NewWithToken(h.url, nil, h.token)
	if _, err := c.ClaimNodeByID(ctx, h.trig.ID, "build", "k8s-job:elsewhere", time.Minute, false, nil); err != nil {
		t.Fatal(err)
	}
	host := &claimedNodeHost{controllerURL: h.url, binary: h.bin, dir: t.TempDir(), ctrl: c, logger: slog.New(slog.DiscardHandler)}
	res := host.RunNode(t.Context(), runner.Request{RunID: h.trig.ID, NodeID: "build", Pipeline: "podnode"})
	if res.Outcome != sparkwing.Failed || !errors.Is(res.Err, store.ErrLockHeld) {
		t.Fatalf("second host = %+v, want refused as held", res)
	}
	if n := h.node(t, "build"); n.Status == "done" || n.ClaimedBy != "k8s-job:elsewhere" {
		t.Fatalf("build = %s claimed by %q, want left to its holder", n.Status, n.ClaimedBy)
	}
	if _, err := os.Stat(envOut); !os.IsNotExist(err) {
		t.Fatalf("the refused host ran the node: %v", err)
	}
}

// An operator's cancel reaches the run through the trigger claim: the
// running node's process is stopped, the run is recorded cancelled rather
// than failed, and the trigger is released.
func TestTriggerHost_OperatorCancelStopsTheRunningNode(t *testing.T) {
	hold, pids := heldNodePID(t)
	h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "hold": hold})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.host(t, ctx, nil, 100*time.Millisecond) }()
	pid := waitForPID(t, ctx, pids, done)
	if err := h.st.RequestCancel(ctx, h.trig.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("host: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the run kept going after its cancel")
	}
	if run := h.run(t); run.Status != "cancelled" {
		t.Fatalf("run = %s (%s), want cancelled", run.Status, run.Error)
	}
	if n := h.node(t, "held"); n.Status != "done" || n.Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("held = %s/%s (%s), want done/cancelled", n.Status, n.Outcome, n.Error)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the held node's process %d outlived the cancel: %v", pid, err)
	}
}

// hack: the held node writes its pid into a fifo this test holds open for both ends, so the read blocks on the node
// rather than the clock, and closing the fifo at cleanup ends the read.
func heldNodePID(t *testing.T) (string, <-chan int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "held")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	pids := make(chan int, 1)
	go func() {
		buf := make([]byte, 32)
		n, err := f.Read(buf)
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(string(buf[:n])); err == nil {
			pids <- pid
		}
	}()
	return path, pids
}

func waitForPID(t *testing.T, ctx context.Context, pids <-chan int, done <-chan error) int {
	t.Helper()
	select {
	case pid := <-pids:
		return pid
	case err := <-done:
		t.Fatalf("the run ended before the held node started: %v", err)
	case <-ctx.Done():
		t.Fatal("the held node never started")
	}
	return 0
}

func dispatchedHeld(t *testing.T, stop func(context.CancelCauseFunc)) *hostedTrigger {
	t.Helper()
	hold, pids := heldNodePID(t)
	args := map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "hold": hold}
	h := newHostedTrigger(t, args)
	planned, err := nodehost.PlanDocument(t.Context(), nodehost.SessionConfig{Entry: h.bin, Dir: t.TempDir()}, nodehost.PlanRequest{
		Pipeline: "podnode", Args: args, Run: podRunContext(h.run(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	for _, n := range planned.Doc.Nodes {
		if n.ID == "held" {
			hash = n.SpecHash
		}
	}
	ctx := store.WithoutCreditMetering(t.Context())
	if err := h.st.CreateNode(ctx, store.Node{RunID: h.trig.ID, NodeID: "held", Status: "pending", SpecHash: hash}); err != nil {
		t.Fatal(err)
	}
	c := client.NewWithToken(h.url, nil, h.token)
	n, err := c.ClaimNodeByID(ctx, h.trig.ID, "held", "pool:held", time.Minute, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	fenced := store.WithNodeClaimFence(t.Context(), store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID, ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	})
	hostCtx, cancel := context.WithCancelCause(fenced)
	defer cancel(nil)
	done := make(chan error, 1)
	go func() {
		_, err := hostDispatchedNode(hostCtx, podNode{
			binary: h.bin, dir: t.TempDir(), controllerURL: h.url, token: n.ClaimToken,
			runID: h.trig.ID, nodeID: "held", logger: slog.New(slog.DiscardHandler),
		})
		done <- err
	}()
	waitCtx, waitCancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer waitCancel()
	pid := waitForPID(t, waitCtx, pids, done)
	stop(cancel)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("host: %v", err)
		}
	case <-waitCtx.Done():
		t.Fatal("the held node outlived its stop")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the held node's process %d outlived its stop: %v", pid, err)
	}
	return h
}

// safety: a runner shutting down stops its node with no cause, and the node
// stays with its claim, so the lease hands it to another runner rather than
// the run failing for a deploy of the runner.
func TestDispatchedNode_ARunnerShuttingDownLeavesTheNodeToItsLease(t *testing.T) {
	h := dispatchedHeld(t, func(cancel context.CancelCauseFunc) { cancel(context.Canceled) })
	if n := h.node(t, "held"); n.Status == "done" || n.ClaimedBy != "pool:held" {
		t.Fatalf("held = %s/%s claimed by %q, want left running under its claim", n.Status, n.Outcome, n.ClaimedBy)
	}
}

// A node stopped for a reason, as a lost claim or a run's cancel stops it,
// is recorded cancelled under its claim.
func TestDispatchedNode_AStopWithACauseRecordsTheNodeCancelled(t *testing.T) {
	h := dispatchedHeld(t, func(cancel context.CancelCauseFunc) { cancel(errors.New("the claim ended")) })
	if n := h.node(t, "held"); n.Status != "done" || n.Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("held = %s/%s (%s), want done/cancelled", n.Status, n.Outcome, n.Error)
	}
}

// safety: a claimed node runs only the pipeline its run's source builds, so
// a run that names no source is refused rather than run from whatever binary
// the runner happens to be.
func TestDispatchedNode_WithoutASourceIsRefused(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateRun(t.Context(), store.Run{ID: "run-sourceless", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, slog.New(slog.DiscardHandler)).Handler())
	t.Cleanup(srv.Close)
	node := &store.Node{RunID: "run-sourceless", NodeID: "build", ClaimedBy: "holder", ClaimGeneration: 1}
	_, err = RunNodeOnce(t.Context(), srv.URL, "", node.RunID, node.NodeID, node.ClaimedBy, "runner-token",
		nil, slog.New(slog.DiscardHandler), nil, ClaimedNodeAttempt(node))
	if err == nil || !strings.Contains(err.Error(), "no repository or working-tree source") {
		t.Fatalf("RunNodeOnce = %v, want the sourceless node refused", err)
	}
}
