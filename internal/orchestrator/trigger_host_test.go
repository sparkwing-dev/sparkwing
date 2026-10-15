//go:build unix

package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/runflags"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
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
	return newHostedTriggerWith(t, args, func(*store.Trigger) {})
}

func newHostedTriggerWith(t *testing.T, args map[string]string, shape func(*store.Trigger)) *hostedTrigger {
	t.Helper()
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	h := &hostedTrigger{bin: podFixture(t)}
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.st = st
	h.token, _, err = st.CreateToken("trigger-runner", store.TokenKindRunner, []string{
		controller.ScopeTriggersClaim, controller.ScopeTriggersRead, controller.ScopeRunsState,
		controller.ScopeRunsRead, controller.ScopeNodesClaim, controller.ScopeLogsRead,
	}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// safety: built after the token exists, because a controller whose tokens table is empty serves every
	// request unauthenticated.
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).EnableAuthFromStore().WithSecretsCipher(cipher).Handler())
	t.Cleanup(srv.Close)
	h.url = srv.URL
	h.claimTrigger(t, "run-hosted", args, shape)
	return h
}

func (h *hostedTrigger) claimTrigger(t *testing.T, id string, args map[string]string, shape func(*store.Trigger)) {
	t.Helper()
	ctx, now := store.WithoutCreditMetering(t.Context()), time.Now()
	trig := store.Trigger{
		ID: id, Pipeline: "podnode", Args: args, Repo: "acme/widgets",
		GitBranch: "main", GitSHA: strings.Repeat("b", 40), CreatedAt: now,
	}
	shape(&trig)
	run := store.Run{
		ID: id, Pipeline: "podnode", Status: "pending", Args: args, CreatedAt: now, StartedAt: now, RetryOf: trig.RetryOf,
		// safety: as the controller's trigger intake records the trigger's run flags on the run row.
		Invocation: runflags.FromTriggerEnv(trig.TriggerEnv).Invocation(),
	}
	if err := h.st.CreateTriggerWithRun(ctx, trig, run); err != nil {
		t.Fatal(err)
	}
	claimed, err := client.NewWithToken(h.url, nil, h.token).ClaimTriggerFor(t.Context(), nil, nil)
	if err != nil || claimed == nil || claimed.ID != id {
		t.Fatalf("claim the trigger: %+v %v", claimed, err)
	}
	h.trig = claimed
}

func (h *hostedTrigger) host(t *testing.T, ctx context.Context, r runner.Runner, heartbeat time.Duration) error {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if heartbeat > 0 {
		c, err := client.NewEngineClient(client.EngineOptions{URL: h.url, Bearer: func(context.Context) (string, error) { return h.token, nil }})
		if err != nil {
			t.Fatal(err)
		}
		keeper := lease.Keeper{Interval: heartbeat, Lease: store.DefaultLeaseDuration, Renew: func(ctx context.Context) (lease.Beat, error) {
			status, err := c.Renew(ctx, h.trig.ID, controllerwire.RenewalRequest{})
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLockHeld) || client.IsTokenDead(err) {
				return lease.Beat{}, &apierr.Error{Code: apierr.LockHeld, Message: "test trigger claim ended"}
			}
			if err != nil {
				return lease.Beat{}, err
			}
			return lease.Beat{CancelRequested: status.CancelRequested}, nil
		}}
		var stop func()
		ctx, stop = keeper.Run(DispatchContext(ctx, h.trig))
		defer stop()
	}
	return HostTriggerRun(ctx, TriggerHost{
		ControllerURL: h.url, Token: h.token, Trigger: h.trig, Entry: pipelineentry.Binary(h.bin), Dir: t.TempDir(),
		Runner: r, Logger: quiet,
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
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "runner-aws")
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
	// safety: network access requires the proxy, while the runner credential stays private.
	if !strings.Contains(string(env), "HTTPS_PROXY=http://proxy.example:3128") || strings.Contains(string(env), "runner-aws") {
		t.Fatalf("the node's environment = \n%s\nwant the runner's HTTPS_PROXY and none of its other settings", env)
	}
	trig, err := h.st.GetTrigger(t.Context(), h.trig.ID)
	if err != nil || trig.Status != "done" {
		t.Fatalf("trigger = %+v %v, want done", trig, err)
	}
}

// safety: planning and the node's own plan read the same operator allow-listed
// variables, so plan code that reads one plans the same in both and the node
// runs instead of failing with plan_drift.
func TestTriggerHost_PlanCodeSeesTheOperatorAllowListAsTheNodeDoes(t *testing.T) {
	writeMachineSettings(t, "run:\n  submit_env_allow: [AWS_REGION]\n")
	t.Setenv("AWS_REGION", "us-west-2")
	envOut := filepath.Join(t.TempDir(), "env")
	h := newHostedTrigger(t, map[string]string{"envout": envOut, "region": "true"})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := h.host(t, ctx, nil, 0); err != nil {
		t.Fatalf("host: %v", err)
	}
	if n := h.node(t, "build"); n.Outcome != string(sparkwing.Success) {
		t.Fatalf("build = %s (%s, %s), want success", n.Outcome, n.FailureReason, n.Error)
	}
	env, err := os.ReadFile(envOut)
	if err != nil || !strings.Contains(string(env), "PLAN_REGION=us-west-2") {
		t.Fatalf("the node's environment = %s (%v), want the planned PLAN_REGION=us-west-2", env, err)
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
	rc, _, err := triggerRunContext(t.Context(), client.NewWithToken(h.url, nil, h.token), h.trig)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := nodehost.PlanDocument(t.Context(), nodehost.SessionConfig{Entry: pipelineentry.Binary(h.bin), Dir: t.TempDir()}, nodehost.PlanRequest{
		Pipeline: "podnode", Args: args, Run: rc,
	})
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	for _, n := range planned.Doc.Jobs {
		if n.ID == "held" {
			raw, err := json.Marshal(n)
			if err != nil {
				t.Fatal(err)
			}
			hash = fmt.Sprintf("%x", sha256.Sum256(raw))
		}
	}
	ctx := store.WithoutCreditMetering(t.Context())
	if err := h.st.UpdatePlanSnapshot(ctx, h.trig.ID, planned.JSON); err != nil {
		t.Fatal(err)
	}
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
			entry: pipelineentry.Binary(h.bin), dir: t.TempDir(), controllerURL: h.url, token: n.ClaimToken,
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
	_, err = RunNodeOnce(t.Context(), srv.URL, node.RunID, node.NodeID, node.ClaimedBy, "runner-token",
		slog.New(slog.DiscardHandler), ClaimedNodeAttempt(node))
	if err == nil || !strings.Contains(err.Error(), "no repository or working-tree source") {
		t.Fatalf("RunNodeOnce = %v, want the sourceless node refused", err)
	}
}

// safety: a guard that reads a secret answers on the run host through the
// trigger's claim, so a guard set to skip skips the deploy rather than fail
// open for want of the secret.
func TestTriggerHost_ASecretBackedSkipIfSkipsTheNode(t *testing.T) {
	guard := filepath.Join(t.TempDir(), "deployed")
	h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "guard": guard})
	admin, _, err := h.st.CreateToken("secrets-owner", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.NewWithToken(h.url, nil, admin).CreateSecretForPipeline(t.Context(), "DEPLOY_GUARD", "skip", "podnode", true, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := h.host(t, ctx, nil, 0); err != nil {
		t.Fatalf("host: %v", err)
	}
	if n := h.node(t, "deploy"); n.Outcome != string(sparkwing.Skipped) {
		t.Fatalf("deploy = %s/%s (%s), want skipped by its guard", n.Status, n.Outcome, n.Error)
	}
	if _, err := os.Stat(guard); !os.IsNotExist(err) {
		t.Fatalf("the guarded deploy ran: %v", err)
	}
}

type runnerFunc func(context.Context, runner.Request) runner.Result

func (f runnerFunc) RunNode(ctx context.Context, req runner.Request) runner.Result {
	return f(ctx, req)
}

// safety: a node another holder won the claim to is that holder's to run and
// finish, so the run host neither runs it nor fails the run: it takes the
// holder's result as the node's.
func TestTriggerHost_TakesTheResultOfTheHolderThatWonTheClaim(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "env")
	h := newHostedTrigger(t, map[string]string{"envout": envOut})
	c := client.NewWithToken(h.url, nil, h.token)
	finished := make(chan error, 1)
	r := runnerFunc(func(ctx context.Context, req runner.Request) runner.Result {
		won, err := c.ClaimNodeByID(store.WithoutClaimFences(ctx), req.RunID, req.NodeID, "pool:legitimate", time.Minute, false, nil)
		if err != nil {
			t.Errorf("claim: %v", err)
			return runner.Result{Outcome: sparkwing.Failed, Err: err}
		}
		host := &claimedNodeHost{controllerURL: h.url, entry: pipelineentry.Binary(h.bin), dir: t.TempDir(), ctrl: c, logger: slog.New(slog.DiscardHandler)}
		res := host.RunNode(ctx, req)
		fenced := store.WithNodeClaimFence(context.WithoutCancel(ctx), store.NodeClaimFence{
			HolderID: won.ClaimedBy, MembershipID: won.ClaimMembershipID, ReservationID: won.ReservationID, ClaimGeneration: won.ClaimGeneration,
		})
		go func() {
			finished <- client.NewWithToken(h.url, nil, won.ClaimToken).FinishNodeWithReason(fenced, req.RunID, req.NodeID, string(sparkwing.Success), "", nil, "", nil)
		}()
		return res
	})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := h.host(t, ctx, r, 0); err != nil {
		t.Fatalf("host: %v", err)
	}
	if err := <-finished; err != nil {
		t.Fatalf("the holder's finish: %v", err)
	}
	if run := h.run(t); run.Status != "success" {
		t.Fatalf("run = %s (%s), want the holder's success", run.Status, run.Error)
	}
	if n := h.node(t, "build"); n.Outcome != string(sparkwing.Success) || n.ClaimedBy != "pool:legitimate" {
		t.Fatalf("build = %s/%s claimed by %q, want the holder's success", n.Status, n.Outcome, n.ClaimedBy)
	}
	if _, err := os.Stat(envOut); !os.IsNotExist(err) {
		t.Fatalf("the refused host ran the node: %v", err)
	}
}

// safety: a runner shutting down is stopped with no cause of its own, so the
// run, its running node and its trigger stay unfinished and claimed, and the
// lease hands them to another runner rather than the run failing for a deploy.
func TestTriggerHost_ShutdownLeavesTheRunToItsLease(t *testing.T) {
	hold, pids := heldNodePID(t)
	h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "hold": hold})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.host(t, ctx, nil, 100*time.Millisecond) }()
	waitCtx, stop := context.WithTimeout(t.Context(), 2*time.Minute)
	defer stop()
	waitForPID(t, waitCtx, pids, done)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("host: %v", err)
		}
	case <-waitCtx.Done():
		t.Fatal("the host did not stop")
	}
	trig, err := h.st.GetTrigger(t.Context(), h.trig.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, n := h.run(t), h.node(t, "held")
	if run.Status != "running" || trig.Status == "done" || n.Status == "done" {
		t.Fatalf("after shutdown run=%s trigger=%s held=%s/%s, want all left to their leases", run.Status, trig.Status, n.Status, n.Outcome)
	}
}

// A dispatched recovery node is handed how its parent failed, as the run
// host recorded it, so rollback code that branches on the failure sees it.
func TestTriggerHost_ADispatchedRecoverySeesItsParentsFailure(t *testing.T) {
	recovered := filepath.Join(t.TempDir(), "recovered")
	h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "recover": recovered})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := h.host(t, ctx, nil, 0); err != nil {
		t.Fatalf("host: %v", err)
	}
	got, err := os.ReadFile(recovered)
	if n := h.node(t, "recover"); err != nil || !strings.Contains(string(got), "broken on purpose") {
		t.Fatalf("recover = %s/%s (%s), recorded %q %v; want the parent's error", n.Status, n.Outcome, n.Error, got, err)
	}
}

// safety: a dispatched node's Job is bounded by every attempt its retries
// allow on its host, and the backoff between them, not by one attempt.
func TestDispatchThrough_BoundsTheNodeByAllItsAttempts(t *testing.T) {
	var got runner.Request
	exec := dispatchThrough(runnerFunc(func(_ context.Context, req runner.Request) runner.Result {
		got = req
		return runner.Result{Outcome: sparkwing.Success}
	}), "p")
	exec(t.Context(), &nodehost.NodeRun{Run: &nodehost.Run{ID: "r"}, Plan: nodeproto.JobSpec{ID: "n", TimeoutMS: testOrchestratorPtr[int64](5 * 60_000), Retry: &nodeproto.RetrySpec{Attempts: 1, BackoffMS: testOrchestratorPtr[int64](6 * 60_000)}, Resources: &nodeproto.ResourceSpec{Cores: testOrchestratorPtr[float64](4), MemoryBytes: testOrchestratorPtr[int64](8 << 30)}}})
	if got.Timeout != 16*time.Minute || got.Cores != 4 || got.MemoryBytes != 8<<30 {
		t.Fatalf("request timeout=%s cores=%v memory=%d, want 16m, 4 cores and 8 GiB", got.Timeout, got.Cores, got.MemoryBytes)
	}
}

// The trigger's actor and pull request reach plan code on the run host and on
// the node's host alike, so a plan that reads them hashes the same in both.
func TestTriggerHost_PlansWithTheTriggersActorAndPullRequest(t *testing.T) {
	envOut := filepath.Join(t.TempDir(), "env")
	h := newHostedTriggerWith(t, map[string]string{"envout": envOut}, func(trig *store.Trigger) {
		trig.TriggerUser, trig.TriggerSource = "alice", "github"
		trig.TriggerEnv = map[string]string{
			"GITHUB_EVENT_NAME": "pull_request", "SPARKWING_PR_NUMBER": "42",
			"SPARKWING_PR_BASE_REF": "main", "SPARKWING_PR_HEAD_REF": "feature",
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := h.host(t, ctx, nil, 0); err != nil {
		t.Fatalf("host: %v", err)
	}
	if n := h.node(t, "build"); n.Outcome != string(sparkwing.Success) {
		t.Fatalf("build = %s/%s (%s), want success with matching plans", n.Status, n.Outcome, n.Error)
	}
	if env, err := os.ReadFile(envOut); err != nil || !strings.Contains(string(env), "ACTOR=alice#42") {
		t.Fatalf("build env lacks ACTOR=alice#42 (%v):\n%s", err, env)
	}
}

// safety: `sparkwing pipeline trigger` carries --sw-only, the step range and
// --sw-dry-run in the trigger's environment, so the runner hosting the run
// cuts the plan and each node host skips the steps the flags put out of range.
func TestTriggerHost_HonorsTheTriggersRunFlags(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		ran     []string
		skipped []string
	}{
		{
			name: "only", env: map[string]string{"SPARKWING_ONLY": "ship"},
			ran: []string{"compile", "package", "prep", "upload"}, skipped: []string{"build", "lint"},
		},
		{
			name: "step range", env: map[string]string{"SPARKWING_START_AT": "package", "SPARKWING_STOP_AT": "package"},
			ran: []string{"lint", "package", "prep"},
		},
		{name: "dry run", env: map[string]string{"SPARKWING_DRY_RUN": "1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			ranged := filepath.Join(dir, "ranged")
			h := newHostedTriggerWith(t, map[string]string{"envout": filepath.Join(dir, "env"), "ranged": ranged},
				func(trig *store.Trigger) { trig.TriggerEnv = c.env })
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			if err := h.host(t, ctx, nil, 0); err != nil {
				t.Fatalf("host: %v", err)
			}
			if run := h.run(t); run.Status != "success" {
				t.Fatalf("run = %s (%s), want success", run.Status, run.Error)
			}
			data, err := os.ReadFile(ranged)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			ran := strings.Fields(string(data))
			slices.Sort(ran)
			if !slices.Equal(ran, c.ran) {
				t.Fatalf("steps that ran = %v, want %v", ran, c.ran)
			}
			for _, id := range c.skipped {
				if n := h.node(t, id); n.Outcome != string(sparkwing.Skipped) {
					t.Errorf("%s = %s/%s, want skipped outside --sw-only", id, n.Status, n.Outcome)
				}
			}
		})
	}
}

// safety: a run's recorded source is whatever surface started it, and the node
// protocol knows four, so a source it does not know plans as the one it means
// rather than failing the plan.
func TestProtocolTriggerSourceNamesEveryRecordedSource(t *testing.T) {
	for recorded, want := range map[string]string{
		"": "manual", "manual": "manual", "cli": "manual", "api": "manual", "runs-submit": "manual",
		"retry": "manual", "await-pipeline": "manual", "pipeline-working-tree@laptop": "manual",
		"schedule": "schedule", "push": "push", "webhook": "webhook", "github": "webhook",
	} {
		if got := protocolTriggerSource(recorded); got != want {
			t.Errorf("protocolTriggerSource(%q) = %q, want %q", recorded, got, want)
		}
	}
}

func TestTriggerHost_AReclaimedTriggerResumesItsRun(t *testing.T) {
	hold, pids := heldNodePID(t)
	h := newHostedTrigger(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "hold": hold})
	ctx, shutdown := context.WithCancel(t.Context())
	defer shutdown()
	done := make(chan error, 1)
	go func() { done <- h.host(t, ctx, nil, 100*time.Millisecond) }()
	waitCtx, stop := context.WithTimeout(t.Context(), 2*time.Minute)
	defer stop()
	waitForPID(t, waitCtx, pids, done)
	shutdown()
	if err := <-done; err != nil {
		t.Fatalf("shutdown host: %v", err)
	}
	if n := h.node(t, "build"); n.Outcome != string(sparkwing.Success) {
		t.Fatalf("build before the reclaim = %s/%s, want success", n.Status, n.Outcome)
	}
	if _, err := h.st.DB().ExecContext(t.Context(), `UPDATE triggers SET lease_expires_at = 1 WHERE id = ?`, h.trig.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.DB().ExecContext(t.Context(), `UPDATE nodes SET lease_expires_at = 1 WHERE run_id = ? AND node_id = 'held'`, h.trig.ID); err != nil {
		t.Fatal(err)
	}
	if recovered, err := teststore.ReapUnits(t.Context(), h.st); err != nil || len(recovered) != 2 {
		t.Fatalf("recovered units = %+v, %v; want node and trigger", recovered, err)
	}
	if n := h.node(t, "held"); n.Status != "pending" || n.ClaimedBy != "" {
		t.Fatalf("held after recovery = %s/%s, want unclaimed pending", n.Status, n.Outcome)
	}
	claimed, err := client.NewWithToken(h.url, nil, h.token).ClaimTriggerFor(t.Context(), nil, nil)
	if err != nil || claimed == nil || claimed.ClaimSeq <= h.trig.ClaimSeq {
		t.Fatalf("reclaim = %+v, %v; want a newer claim", claimed, err)
	}
	h.trig = claimed
	var dispatched []string
	r := runnerFunc(func(ctx context.Context, req runner.Request) runner.Result {
		dispatched = append(dispatched, req.NodeID)
		n, err := client.NewWithToken(h.url, nil, h.token).ClaimNodeByID(store.WithoutClaimFences(ctx), req.RunID, req.NodeID, "successor", time.Minute, false, nil)
		if err != nil {
			return runner.Result{Outcome: sparkwing.Failed, Err: err}
		}
		fenced := store.WithNodeClaimFence(store.WithoutClaimFences(ctx), store.NodeClaimFence{
			HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID, ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
		})
		if err := client.NewWithToken(h.url, nil, n.ClaimToken).FinishNodeWithReason(fenced, req.RunID, req.NodeID, string(sparkwing.Success), "", nil, "", nil); err != nil {
			return runner.Result{Outcome: sparkwing.Failed, Err: err}
		}
		return runner.Result{Outcome: sparkwing.Success}
	})
	resumeCtx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := h.host(t, resumeCtx, r, 0); err != nil {
		t.Fatalf("resumed host: %v", err)
	}
	trig, err := h.st.GetTrigger(t.Context(), h.trig.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run := h.run(t); run.Status != "success" || trig.Status != "done" || !slices.Equal(dispatched, []string{"held"}) {
		t.Fatalf("resumed run = %s (%s), trigger %s, dispatched %v; want success with only held run again", run.Status, run.Error, trig.Status, dispatched)
	}
}

// safety: a network proxy URL can carry a credential, which the node
// receives to reach the proxy but which no stored log may show.
func TestTriggerHost_MasksTheProxyCredentialsANodeReceives(t *testing.T) {
	dir := t.TempDir()
	envOut, echo := filepath.Join(dir, "env"), filepath.Join(dir, "echo")
	values := map[string]string{
		"HTTPS_PROXY": "https://a:go-one-password@one.invalid/golang|https://b:go-two-password@two.invalid/golang,direct",
		"HTTP_PROXY":  "https://cache-reader:npm-prefix,npm-suffix@cache.invalid/proxy/npm",
		"https_proxy": "https://cache-reader:s%61fe-pip-password@cache.invalid/proxy/pypi/simple",
	}
	var echoed []string
	for name, value := range values {
		t.Setenv(name, value)
		echoed = append(echoed, name+"="+value)
	}
	// safety: a node may log one GOPROXY member on its own, or a password decoded.
	echoed = append(echoed, "https://a:go-one-password@one.invalid/golang", "safe-pip-password")
	if err := os.WriteFile(echo, []byte(strings.Join(echoed, " ")), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHostedTrigger(t, map[string]string{"envout": envOut, "echo": echo})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := h.host(t, ctx, nil, 0); err != nil {
		t.Fatalf("host: %v", err)
	}
	env, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.url+"/api/v1/runs/"+url.PathEscape(h.trig.ID)+"/logs/build?format=raw", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	logs, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if resp.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
		t.Fatalf("log status=%d errors=%v, %v", resp.StatusCode, readErr, closeErr)
	}
	for name, value := range values {
		if !strings.Contains(string(env), name+"="+value) {
			t.Errorf("the node did not receive %s", name)
		}
	}
	for _, secret := range []string{"go-one-password", "go-two-password", "npm-prefix,npm-suffix", "npm-suffix", "s%61fe-pip-password", "safe-pip-password"} {
		if strings.Contains(string(logs), secret) {
			t.Errorf("the build log shows %s", secret)
		}
	}
	if !strings.Contains(string(logs), "HTTPS_PROXY=***") {
		t.Errorf("the build log = %s, want the echoed proxy settings masked", logs)
	}
}
