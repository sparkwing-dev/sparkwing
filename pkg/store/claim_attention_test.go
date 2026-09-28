package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func ageReadyNode(t *testing.T, s *store.Store, runID string, age time.Duration) {
	t.Helper()
	if _, err := s.DB().ExecContext(context.Background(), storetest.Rebind(s,
		`UPDATE nodes SET ready_at = ?, placement_hold_from = ? WHERE run_id = ?`),
		time.Now().Add(-age).UnixNano(), time.Now().Add(-age).UnixNano(), runID); err != nil {
		t.Fatal(err)
	}
}

// A node that needs a tool is never taken by an agent without it, by queue or
// by name, and the Cloud pool has exactly the tools its image declares.
func TestToolNodeIsNeverClaimedByAnAgentWithoutTheTool(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedMatchNode(t, s, "run-tf", "", 1, "tool:terraform")
	agent := store.ClaimIdentity{Principal: "agent:pi", TokenPrefix: "swr_pi"}
	if _, err := s.ClaimNextReadyNode(ctx, agent, "pi:1", time.Minute, []string{"tool:git", "tool:go"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a queue claim without terraform = %v, want not found", err)
	}
	if _, err := s.ClaimNamedNode(ctx, agent, "run-tf", "work", "pi:2", time.Minute,
		store.NamedClaimOptions{Labels: []string{"tool:git"}}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("a named claim without terraform = %v, want held", err)
	}
	pool := meteredClaimant(t, s, "agent:pool")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent, "pay_1", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNamedNode(ctx, pool, "run-tf", "work", "k8s-job:1", time.Minute,
		store.NamedClaimOptions{Labels: []string{"tool:terraform"}}); err == nil {
		t.Fatal("the Cloud pool claimed a terraform node its image does not declare")
	}
	seedMatchNode(t, s, "run-go", "", 1, "tool:go")
	if n, err := s.ClaimNamedNode(ctx, pool, "run-go", "work", "k8s-job:2", time.Minute,
		store.NamedClaimOptions{}); err != nil || n.ClaimedBy != "k8s-job:2" {
		t.Fatalf("the Cloud pool's claim of a node needing a declared tool = %+v, %v", n, err)
	}
	if n, err := s.ClaimNextReadyNode(ctx, agent, "pi:3", time.Minute, []string{"tool:terraform"}); err != nil || n.RunID != "run-tf" {
		t.Fatalf("a queue claim with terraform = %+v, %v; want run-tf", n, err)
	}
}

// The attention reason shows on the run while the node waits and is gone the
// moment an agent claims it.
func TestNodeAttentionShowsOnTheRunUntilAClaim(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedMatchNode(t, s, "run-tf", "", 1, "tool:terraform")
	ageReadyNode(t, s, "run-tf", time.Minute)
	waiting, err := s.ListWaitingNodes(ctx, time.Now().Add(-30*time.Second), [2]string{}, 10)
	if err != nil || len(waiting) != 1 || !slices.Equal(waiting[0].Selector, []string{"tool:terraform"}) {
		t.Fatalf("waiting = %+v, %v", waiting, err)
	}
	const reason = "node work needs tool:terraform; no agent in team default has it"
	if err := s.SetNodeAttention(ctx, []store.NodeAttention{{Team: store.DefaultTeam, RunID: "run-tf", NodeID: "work", Reason: reason}}); err != nil {
		t.Fatal(err)
	}
	run, err := s.GetRun(ctx, "run-tf")
	if err != nil || run.NeedsAttention != reason {
		t.Fatalf("run = %+v, %v; want the attention reason", run, err)
	}
	runs, err := s.ListRuns(ctx, store.RunFilter{Limit: 10})
	if err != nil || len(runs) != 1 || runs[0].NeedsAttention != reason {
		t.Fatalf("run list = %+v, %v; want the attention reason", runs, err)
	}
	agent := store.ClaimIdentity{Principal: "agent:box", TokenPrefix: "swr_box"}
	if _, err := s.ClaimNextReadyNode(ctx, agent, "box:1", time.Minute, []string{"tool:terraform"}); err != nil {
		t.Fatal(err)
	}
	if run, err = s.GetRun(ctx, "run-tf"); err != nil || run.NeedsAttention != "" {
		t.Fatalf("run after a claim = %+v, %v; want no attention", run, err)
	}
}

// A ready node fails as unclaimable, with its reason, only once its run's
// claim wait has passed: 24 hours by default, or what the plan set.
func TestUnclaimedNodesFailAtTheirClaimWait(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedMatchNode(t, s, "run-default", "", 1, "tool:terraform")
	seedMatchNode(t, s, "run-short", "", 1, "tool:terraform")
	if err := s.UpdatePlanSnapshot(ctx, "run-short", []byte(`{"claim_wait_ms":3600000,"nodes":[]}`)); err != nil {
		t.Fatal(err)
	}
	ageReadyNode(t, s, "run-default", 2*time.Hour)
	ageReadyNode(t, s, "run-short", 2*time.Hour)
	// safety: an agent that cannot take the nodes polls, moving ready_at forward;
	// the waits still run from when each node first became ready.
	for i := range 3 {
		if _, err := s.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "agent:pi", TokenPrefix: "swr_pi"},
			"pi:"+string(rune('a'+i)), time.Minute, []string{"tool:git"}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("an ineligible poll = %v", err)
		}
	}
	if waiting, err := s.ListWaitingNodes(ctx, time.Now().Add(-time.Hour), [2]string{}, 10); err != nil || len(waiting) != 2 {
		t.Fatalf("waiting after ineligible polls = %+v, %v; want both nodes", waiting, err)
	}
	if err := s.SetNodeAttention(ctx, []store.NodeAttention{{
		Team: store.DefaultTeam, RunID: "run-short", NodeID: "work", Reason: "node work needs tool:terraform",
	}}); err != nil {
		t.Fatal(err)
	}
	pairs, err := store.Maintenance.FailStaleQueuedNodes(s, ctx, match.DefaultClaimWait)
	if err != nil || len(pairs) != 1 || pairs[0] != [2]string{"run-short", "work"} {
		t.Fatalf("failed = %v, %v; want only the node past its one-hour wait", pairs, err)
	}
	n, err := s.GetNode(ctx, "run-short", "work")
	if err != nil || n.Error != "unclaimable: node work needs tool:terraform" || n.FailureReason != store.FailureQueueTimeout {
		t.Fatalf("node = %+v, %v; want unclaimable with its reason", n, err)
	}
	ageReadyNode(t, s, "run-default", 25*time.Hour)
	if pairs, err = store.Maintenance.FailStaleQueuedNodes(s, ctx, match.DefaultClaimWait); err != nil || len(pairs) != 1 {
		t.Fatalf("failed after 25h = %v, %v; want the default-wait node", pairs, err)
	}
	if n, _ = s.GetNode(ctx, "run-default", "work"); !strings.HasPrefix(n.Error, "unclaimable: ") {
		t.Fatalf("node error = %q", n.Error)
	}
}

// A team's agents are listed with what they last advertised, offline or not.
func TestRegisteredAgentsCarryTheirLastLabels(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	_, pi, err := s.CreateToken("agent:pi", store.TokenKindRunner, []string{"nodes.claim"}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateToken("someone", store.TokenKindUser, []string{"runs.read"}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAgentLabels(ctx, pi.Prefix, []string{"tool:git", "name=forged", "arch=arm64"}); err != nil {
		t.Fatal(err)
	}
	agents, err := s.ListRegisteredAgents(ctx, store.DefaultTeam)
	if err != nil || len(agents) != 1 || agents[0].Name != "pi" || agents[0].Enrolled ||
		!slices.Equal(agents[0].Profile.Labels, []string{"tool:git", "arch=arm64"}) {
		t.Fatalf("agents = %+v, %v; want pi with its labels less the forged name", agents, err)
	}
}

// Waiting nodes come back in pages, so one sweep tick reads a bounded batch.
func TestWaitingNodesPage(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	for _, run := range []string{"run-a", "run-b", "run-c"} {
		seedMatchNode(t, s, run, "", 1, "tool:terraform")
		ageReadyNode(t, s, run, time.Minute)
	}
	first, err := s.ListWaitingNodes(ctx, time.Now(), [2]string{}, 2)
	if err != nil || len(first) != 2 || first[1].RunID != "run-b" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	rest, err := s.ListWaitingNodes(ctx, time.Now(), [2]string{first[1].RunID, first[1].NodeID}, 2)
	if err != nil || len(rest) != 1 || rest[0].RunID != "run-c" {
		t.Fatalf("second page = %+v, %v", rest, err)
	}
}

// The sweep writes one row per statement: while another transaction holds one
// node's lock, the reason for a different node is already committed rather
// than held behind it.
func TestSetNodeAttentionHoldsOneNodeLockAtATime(t *testing.T) {
	s := storetest.Open(t)
	if s.Dialect() != store.DialectPostgres {
		t.Skip("SQLite serializes every writer, so there is no row lock to order")
	}
	ctx := context.Background()
	seedMatchNode(t, s, "run-x", "", 1, "tool:terraform")
	seedMatchNode(t, s, "run-y", "", 1, "tool:terraform")
	holder, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback() }()
	if _, err := holder.ExecContext(ctx, `SELECT 1 FROM nodes WHERE run_id = 'run-y' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- s.SetNodeAttention(ctx, []store.NodeAttention{
			{Team: store.DefaultTeam, RunID: "run-x", NodeID: "work", Reason: "x"},
			{Team: store.DefaultTeam, RunID: "run-y", NodeID: "work", Reason: "y"},
		})
	}()
	// safety: once the sweep waits on run-y's lock it has written run-x, so
	// run-x's reason must already be visible to another reader.
	blocked := 0
	for range 100000 {
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
 WHERE wait_event_type = 'Lock' AND query LIKE 'UPDATE nodes SET attention_reason%'`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			break
		}
	}
	if blocked == 0 {
		t.Fatal("the sweep never waited on run-y's lock")
	}
	if run, err := s.GetRun(ctx, "run-x"); err != nil || run.NeedsAttention != "x" {
		t.Fatalf("run-x = %+v, %v; its reason is not committed while run-y's lock is held, so the sweep holds both", run, err)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if run, err := s.GetRun(ctx, "run-y"); err != nil || run.NeedsAttention != "y" {
		t.Fatalf("run-y = %+v, %v; want its reason once the lock is released", run, err)
	}
}

// A node withdrawn from the queue between the sweep's read and its write keeps
// no reason.
func TestSetNodeAttentionSkipsANodeRevokedSinceTheRead(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	seedMatchNode(t, s, "run-r", "", 1, "tool:terraform")
	ageReadyNode(t, s, "run-r", time.Minute)
	waiting, err := s.ListWaitingNodes(ctx, time.Now(), [2]string{}, 10)
	if err != nil || len(waiting) != 1 {
		t.Fatalf("waiting = %+v, %v", waiting, err)
	}
	if revoked, err := s.RevokeNodeReady(ctx, "run-r", "work"); err != nil || !revoked {
		t.Fatalf("revoke = %v, %v", revoked, err)
	}
	if err := s.SetNodeAttention(ctx, []store.NodeAttention{{
		Team: waiting[0].Team, RunID: "run-r", NodeID: "work", Reason: "stale",
	}}); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := s.DB().QueryRowContext(ctx, storetest.Rebind(s,
		`SELECT attention_reason FROM nodes WHERE run_id = ? AND node_id = ?`), "run-r", "work").Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "" {
		t.Fatalf("a revoked node was given the reason %q", reason)
	}
}

// Only the launcher claims a controller-dispatched node, so the attention
// sweep, which judges waits by the agents that could claim, skips it.
func TestWaitingNodesSkipAControllerDispatchedNode(t *testing.T) {
	f := newDispatchRun(t, "run-dispatched")
	f.mustAccept(t, planOf(`a`))
	waiting, err := f.s.ListWaitingNodes(context.Background(), time.Now().Add(time.Hour), [2]string{}, 10)
	if err != nil || len(waiting) != 0 {
		t.Fatalf("waiting = %+v, %v; want none", waiting, err)
	}
}
