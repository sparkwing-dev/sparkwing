package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func setLimit(t *testing.T, s *store.Store, name string, value int64) {
	t.Helper()
	if err := s.SetComputeLimit(context.Background(), name, value); err != nil {
		t.Fatalf("set %s: %v", name, err)
	}
}

// safety: values are inlined because the two dialects spell bound parameters
// differently and every one here is a test-owned integer or identifier.
func rewindRunStart(t *testing.T, s *store.Store, runID string, at time.Time) {
	t.Helper()
	if _, err := s.DB().Exec(fmt.Sprintf(
		`UPDATE runs SET started_at = %d WHERE id = '%s'`, at.UnixNano(), runID)); err != nil {
		t.Fatalf("rewind the run start: %v", err)
	}
}

func TestComputeLimitsAreUnlimitedUntilSet(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	limits, err := s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits != (store.ComputeLimits{}) {
		t.Fatalf("a fresh store reads %+v, want every guard at zero", limits)
	}
	for _, name := range store.ComputeLimitNames() {
		if v, ok := limits.Value(name); !ok || v != 0 {
			t.Fatalf("guard %s = %d (known %v), want 0", name, v, ok)
		}
	}
	seedClaimedNode(t, s, "run-open", "build")
	if err := s.CreateNode(ctx, store.Node{RunID: "run-open", NodeID: "test", Status: "pending"}); err != nil {
		t.Fatalf("an unset guard refused a node: %v", err)
	}
}

func TestSetComputeLimitRejectsUnknownAndNegative(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if err := s.SetComputeLimit(ctx, "max_dollars", 5); err == nil {
		t.Fatal("an unknown guard was accepted")
	}
	if err := s.SetComputeLimit(ctx, store.ComputeLimitNodesPerRun, -1); err == nil {
		t.Fatal("a negative ceiling was accepted")
	}
	setLimit(t, s, store.ComputeLimitNodesPerRun, 7)
	limits, err := s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits.NodesPerRun != 7 {
		t.Fatalf("nodes per run = %d, want 7", limits.NodesPerRun)
	}
	setLimit(t, s, store.ComputeLimitNodesPerRun, 0)
	limits, err = s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits.NodesPerRun != 0 {
		t.Fatalf("nodes per run = %d, want the guard removed", limits.NodesPerRun)
	}
}

func TestNodesPerRunGuardRefusesTheNodePastTheCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setLimit(t, s, store.ComputeLimitNodesPerRun, 2)
	seedClaimedNode(t, s, "run-fan", "build")

	if err := s.CreateNode(ctx, store.Node{RunID: "run-fan", NodeID: "fan-1", Status: "pending"}); err != nil {
		t.Fatalf("the second node must fit under a cap of two: %v", err)
	}
	err := s.CreateNode(ctx, store.Node{RunID: "run-fan", NodeID: "fan-2", Status: "pending"})
	if !errors.Is(err, store.ErrComputeLimit) {
		t.Fatalf("the third node = %v, want ErrComputeLimit", err)
	}
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) || refused.Limit != store.ComputeLimitNodesPerRun {
		t.Fatalf("refusal %v does not name the nodes-per-run guard", err)
	}
	if refused.Cap != 2 || refused.Observed != 2 {
		t.Fatalf("refusal = %+v, want cap 2 observed 2", refused)
	}
	nodes, err := s.ListNodes(ctx, "run-fan")
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want the refused one absent", len(nodes))
	}
}

func TestRunsPerHourGuardRefusesTheRunPastTheCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setLimit(t, s, store.ComputeLimitRunsPerHour, 1)

	if err := s.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("the first run must be allowed: %v", err)
	}
	err := s.CreateRun(ctx, store.Run{
		ID: "run-2", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	})
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) || refused.Limit != store.ComputeLimitRunsPerHour {
		t.Fatalf("the second run = %v, want the runs-per-hour guard", err)
	}

	// safety: the window is the hour before the new run, so a run older than
	// it counts for nothing.
	rewindRunStart(t, s, "run-1", time.Now().Add(-2*time.Hour))
	if _, err := s.DB().Exec(`UPDATE runs SET created_at = 0 WHERE id = 'run-1'`); err != nil {
		t.Fatalf("clear the creation stamp: %v", err)
	}
	if err := s.CreateRun(ctx, store.Run{
		ID: "run-3", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("a run outside the window must not count: %v", err)
	}
}

func TestConcurrentRunnerGuardRefusesTheSecondClaimForOnePrincipal(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-a", "build")
	readyNode(t, s, "run-b", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	setLimit(t, s, store.ComputeLimitConcurrentRunners, 1)

	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("the first claim must succeed: %v", err)
	}
	_, err := s.ClaimNextReadyNode(ctx, claimant, "pod-2", time.Minute, nil)
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) || refused.Limit != store.ComputeLimitConcurrentRunners {
		t.Fatalf("the second claim = %v, want the concurrent-runner guard", err)
	}
	usage, err := s.ComputeUsage(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Runners != 1 || usage.ByPrincipal["agent:cloud"] != 1 {
		t.Fatalf("usage = %+v, want one runner for the claiming principal", usage)
	}
}

func TestGlobalRunnerGuardCountsEveryPrincipal(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	first := meteredClaimant(t, s, "agent:cloud-a")
	second := meteredClaimant(t, s, "agent:cloud-b")
	readyNode(t, s, "run-a", "build")
	readyNode(t, s, "run-b", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	setLimit(t, s, store.ComputeLimitGlobalRunners, 1)
	setLimit(t, s, store.ComputeLimitRunnerAlarm, 1)

	if _, err := s.ClaimNextReadyNode(ctx, first, "pod-a", time.Minute, nil); err != nil {
		t.Fatalf("the first claim must succeed: %v", err)
	}
	_, err := s.ClaimNextReadyNode(ctx, second, "pod-b", time.Minute, nil)
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) || refused.Limit != store.ComputeLimitGlobalRunners {
		t.Fatalf("the claim by a second principal = %v, want the global guard", err)
	}
	usage, err := s.ComputeUsage(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if !usage.AlarmReached {
		t.Fatalf("usage = %+v, want the alarm reached", usage)
	}
}

func TestWallClockGuardRefusesAClaimOnAnOldRun(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-long", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	setLimit(t, s, store.ComputeLimitRunSeconds, 60)
	rewindRunStart(t, s, "run-long", time.Now().Add(-10*time.Minute))

	_, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) || refused.Limit != store.ComputeLimitRunSeconds {
		t.Fatalf("a claim on a run past the guard = %v, want the wall-clock guard", err)
	}
	ceiling, over, err := s.RunExceedsWallClock(ctx, "run-long", time.Now())
	if err != nil {
		t.Fatalf("wall clock: %v", err)
	}
	if !over || ceiling != 60 {
		t.Fatalf("wall clock = (%d, %v), want (60, true)", ceiling, over)
	}
}

func TestCancelNodeForComputeLimitFailsTheNodeAndReleasesTheClaim(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-cut", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if err := s.CancelNodeForComputeLimit(ctx, "run-cut", "build",
		claimant.TokenPrefix, store.ComputeLimitRunSeconds, time.Now()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	node, err := s.GetNode(ctx, "run-cut", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.FailureReason != store.FailureComputeLimit {
		t.Fatalf("failure reason = %q, want %q", node.FailureReason, store.FailureComputeLimit)
	}
	if node.Claimed {
		t.Fatal("a cancelled node kept its claim")
	}
}
