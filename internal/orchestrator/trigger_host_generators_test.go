//go:build unix

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func fanOutArgs(t *testing.T, targets string) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "fanlog")
	return map[string]string{"envout": filepath.Join(dir, "env"), "fanout": targets, "fanlog": log, "fanfail": filepath.Join(dir, "fail")}, log
}

func fanBodies(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	lines := strings.Fields(string(data))
	slices.Sort(lines)
	return lines
}

func (h *hostedTrigger) hostToEnd(t *testing.T, r nodehost.Executor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := h.host(t, ctx, r, 0); err != nil {
		t.Fatalf("host: %v", err)
	}
}

// A runner-hosted run's generator source runs on its node's host, the run
// host generates its nodes from the recorded output, and each generated node
// is dispatched under a claim of its own with the spec_hash the run host
// recorded, so the summary that needs them runs once they all finish.
func TestTriggerHost_DispatchesAGeneratorsNodesAndTheirConsumer(t *testing.T) {
	args, log := fanOutArgs(t, "a,b")
	h := newHostedTrigger(t, args)
	h.hostToEnd(t, nil)
	if run := h.run(t); run.Status != "success" {
		t.Fatalf("run = %s (%s), want success", run.Status, run.Error)
	}
	for _, id := range []string{"targets", "test-a", "test-b", "summary"} {
		n := h.node(t, id)
		if n.Outcome != string(sparkwing.Success) || n.SpecHash == "" || !strings.HasPrefix(n.ClaimedBy, claimedNodeHolder) {
			t.Fatalf("%s = %s/%s (%s) hash %q claimed by %q, want a success under its own node claim", id, n.Status, n.Outcome, n.Error, n.SpecHash, n.ClaimedBy)
		}
	}
	if g := h.node(t, "test-a").Generated; g == nil || g.Source != "targets" || g.Batch != 2 || len(g.Spec) == 0 {
		t.Fatalf("test-a generated record = %+v, want its spec recorded from targets' batch of 2", g)
	}
	if deps := h.node(t, "summary").Deps; !slices.Contains(deps, "test-a") || !slices.Contains(deps, "test-b") {
		t.Fatalf("summary deps = %v, want the generated nodes", deps)
	}
	if got, want := fanBodies(t, log), []string{"summary", "targets", "test-a", "test-b"}; !slices.Equal(got, want) {
		t.Fatalf("bodies = %v, want %v once each", got, want)
	}
}

// A generated node that fails fails the run and cancels the node that needs
// it, and a manual retry copies the generator source and the generated node
// that succeeded, generating the source's nodes again on the run host, and
// runs only the failed node and its consumer.
func TestTriggerHost_AFailedGeneratedNodeFailsTheRunAndARetryRunsOnlyIt(t *testing.T) {
	args, log := fanOutArgs(t, "a,fail")
	if err := os.WriteFile(args["fanfail"], nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHostedTrigger(t, args)
	h.hostToEnd(t, nil)
	if run := h.run(t); run.Status != "failed" {
		t.Fatalf("run = %s, want failed", run.Status)
	}
	if n := h.node(t, "test-fail"); n.Outcome != string(sparkwing.Failed) || !strings.Contains(n.Error, "target fail is broken") {
		t.Fatalf("test-fail = %s/%s (%s), want its body's failure", n.Status, n.Outcome, n.Error)
	}
	if n := h.node(t, "summary"); n.Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("summary = %s/%s, want cancelled behind the failure", n.Status, n.Outcome)
	}
	if err := os.Remove(args["fanfail"]); err != nil {
		t.Fatal(err)
	}
	source := h.trig.ID
	h.claimTrigger(t, "run-retry", args, func(trig *store.Trigger) { trig.RetryOf = source })
	h.hostToEnd(t, nil)
	if run := h.run(t); run.Status != "success" {
		t.Fatalf("retry = %s (%s), want success", run.Status, run.Error)
	}
	for _, id := range []string{"targets", "test-a", "test-fail", "summary"} {
		if n := h.node(t, id); n.Outcome != string(sparkwing.Success) {
			t.Fatalf("retry %s = %s/%s (%s), want success", id, n.Status, n.Outcome, n.Error)
		}
	}
	want := []string{"summary", "targets", "test-a", "test-fail", "test-fail"}
	if got := fanBodies(t, log); !slices.Equal(got, want) {
		t.Fatalf("bodies = %v, want %v: the retry runs only test-fail and summary", got, want)
	}
}

// A generator source that a memo hit copies has its nodes generated on the
// run host from the copied output, and they are dispatched as in the run
// that ran it.
func TestTriggerHost_AMemoizedGeneratorsNodesAreDispatched(t *testing.T) {
	args, log := fanOutArgs(t, "a,b")
	args["fanmemo"] = "true"
	h := newHostedTrigger(t, args)
	h.hostToEnd(t, nil)
	if run := h.run(t); run.Status != "success" {
		t.Fatalf("first run = %s (%s), want success", run.Status, run.Error)
	}
	h.claimTrigger(t, "run-memo", args, func(*store.Trigger) {})
	h.hostToEnd(t, nil)
	if run := h.run(t); run.Status != "success" {
		t.Fatalf("memo run = %s (%s), want success", run.Status, run.Error)
	}
	if n := h.node(t, "targets"); n.Outcome != string(sparkwing.Memoized) {
		t.Fatalf("targets = %s/%s, want the memo hit", n.Status, n.Outcome)
	}
	for _, id := range []string{"test-a", "test-b", "summary"} {
		if n := h.node(t, id); n.Outcome != string(sparkwing.Success) || !strings.HasPrefix(n.ClaimedBy, claimedNodeHolder) {
			t.Fatalf("memo run %s = %s/%s (%s) claimed by %q, want a dispatched success", id, n.Status, n.Outcome, n.Error, n.ClaimedBy)
		}
	}
	want := []string{"summary", "summary", "targets", "test-a", "test-a", "test-b", "test-b"}
	if got := fanBodies(t, log); !slices.Equal(got, want) {
		t.Fatalf("bodies = %v, want %v: targets runs once", got, want)
	}
}

// safety: a reclaimed trigger whose generator and generated nodes already
// finished generates them again on the new host from the recorded output,
// keeps their rows, whose spec_hash matches, and runs only the unfinished node.
func TestTriggerHost_AResumedRunKeepsAFinishedGeneratorsNodes(t *testing.T) {
	hold, pids := heldNodePID(t)
	args, log := fanOutArgs(t, "a,b")
	args["hold"] = hold
	h := newHostedTrigger(t, args)
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
	for _, q := range []string{
		`UPDATE triggers SET lease_expires_at = 1 WHERE id = ?`,
		`UPDATE nodes SET lease_expires_at = 1 WHERE run_id = ? AND node_id = 'held'`,
	} {
		if _, err := h.st.DB().ExecContext(t.Context(), q, h.trig.ID); err != nil {
			t.Fatal(err)
		}
	}
	if recovered, err := teststore.ReapUnits(t.Context(), h.st); err != nil || len(recovered) != 2 {
		t.Fatalf("recovered units = %+v, %v; want node and trigger", recovered, err)
	}
	claimed, err := client.NewWithToken(h.url, nil, h.token).ClaimTriggerFor(t.Context(), nil, nil)
	if err != nil || claimed == nil {
		t.Fatalf("reclaim = %+v, %v", claimed, err)
	}
	h.trig = claimed
	var dispatched []string
	r := nodehost.Executor(func(ctx context.Context, req *nodehost.NodeRun) nodehost.Result {
		dispatched = append(dispatched, req.Plan.ID)
		n, err := client.NewWithToken(h.url, nil, h.token).ClaimNodeByID(store.WithoutClaimFences(ctx), req.Run.ID, req.Plan.ID, "successor", time.Minute, false, nil)
		if err != nil {
			return nodehost.Result{Outcome: sparkwing.Failed, Err: err}
		}
		fenced := store.WithNodeClaimFence(store.WithoutClaimFences(ctx), store.NodeClaimFence{
			HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID, ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
		})
		if err := client.NewWithToken(h.url, nil, n.ClaimToken).FinishNodeWithReason(fenced, req.Run.ID, req.Plan.ID, string(sparkwing.Success), "", nil, "", nil); err != nil {
			return nodehost.Result{Outcome: sparkwing.Failed, Err: err}
		}
		return nodehost.Result{Outcome: sparkwing.Success}
	})
	h.hostToEnd(t, r)
	if run := h.run(t); run.Status != "success" || !slices.Equal(dispatched, []string{"held"}) {
		t.Fatalf("resumed run = %s (%s), dispatched %v; want success with only held run again", run.Status, run.Error, dispatched)
	}
	if got, want := fanBodies(t, log), []string{"summary", "targets", "test-a", "test-b"}; !slices.Equal(got, want) {
		t.Fatalf("bodies = %v, want %v once each", got, want)
	}
}
