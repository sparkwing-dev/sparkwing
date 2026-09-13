package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

func rewindRunCreation(t *testing.T, s *store.Store, runID string, at time.Time) {
	t.Helper()
	if _, err := s.DB().Exec(fmt.Sprintf(
		`UPDATE runs SET created_at = %d WHERE id = '%s'`, at.UnixNano(), runID)); err != nil {
		t.Fatalf("rewind the run creation: %v", err)
	}
}

func createRunAs(t *testing.T, s *store.Store, principal, runID string) error {
	t.Helper()
	ctx := store.WithCreatingPrincipal(context.Background(), principal)
	return s.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	})
}

func limitRefusal(t *testing.T, err error, want string) *store.ComputeLimitError {
	t.Helper()
	var refused *store.ComputeLimitError
	if !errors.As(err, &refused) {
		t.Fatalf("error %v is not a compute limit refusal", err)
	}
	if refused.Limit != want {
		t.Fatalf("refusal names %q, want %q", refused.Limit, want)
	}
	if !errors.Is(err, store.ErrComputeLimit) {
		t.Fatalf("refusal %v does not match ErrComputeLimit", err)
	}
	return refused
}

func TestComputeLimitsAreUnlimitedUntilSet(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	limits, err := s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits.Any() {
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
	if limits.Any() {
		t.Fatalf("limits = %+v, want the guard removed", limits)
	}
}

func TestNodesPerRunGuardRefusesAMeteredPrincipalPastTheCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	setLimit(t, s, store.ComputeLimitNodesPerRun, 2)
	if err := createRunAs(t, s, claimant.Principal, "run-fan"); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run-fan", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	if err := s.CreateNode(ctx, store.Node{RunID: "run-fan", NodeID: "fan-1", Status: "pending"}); err != nil {
		t.Fatalf("the second node must fit under a cap of two: %v", err)
	}
	err := s.CreateNode(ctx, store.Node{RunID: "run-fan", NodeID: "fan-2", Status: "pending"})
	refused := limitRefusal(t, err, store.ComputeLimitNodesPerRun)
	if refused.Cap != 2 || refused.Observed != 2 || refused.Principal != claimant.Principal {
		t.Fatalf("refusal = %+v, want cap 2 observed 2 for the creating principal", refused)
	}
	nodes, err := s.ListNodes(ctx, "run-fan")
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want the refused one absent", len(nodes))
	}
}

func TestPerPrincipalGuardsLeaveLocalWorkAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setLimit(t, s, store.ComputeLimitNodesPerRun, 1)
	setLimit(t, s, store.ComputeLimitRunsPerHour, 1)
	seedClaimedNode(t, s, "run-local", "build")

	if err := s.CreateNode(ctx, store.Node{RunID: "run-local", NodeID: "second", Status: "pending"}); err != nil {
		t.Fatalf("a local run's node was refused: %v", err)
	}
	for _, id := range []string{"run-local-2", "run-local-3"} {
		if err := s.CreateRun(ctx, store.Run{
			ID: id, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		}); err != nil {
			t.Fatalf("a local run was refused: %v", err)
		}
	}
}

func TestGlobalNodesPerRunGuardCountsEveryRun(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setLimit(t, s, store.ComputeLimitGlobalNodesPerRun, 1)
	seedClaimedNode(t, s, "run-any", "build")

	err := s.CreateNode(ctx, store.Node{RunID: "run-any", NodeID: "second", Status: "pending"})
	limitRefusal(t, err, store.ComputeLimitGlobalNodesPerRun)
}

func TestGuardsLetAnIdempotentRecreateThrough(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setLimit(t, s, store.ComputeLimitGlobalNodesPerRun, 1)
	setLimit(t, s, store.ComputeLimitGlobalRunsPerHour, 1)
	seedClaimedNode(t, s, "run-again", "build")

	if err := s.CreateRun(ctx, store.Run{
		ID: "run-again", Pipeline: "demo", Status: "failed", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("re-creating the same run must not meet the hourly guard: %v", err)
	}
	// safety: re-creating a node must fail on the row that already exists, not
	// on the guard, so the error is read rather than merely tolerated.
	err := s.CreateNode(ctx, store.Node{RunID: "run-again", NodeID: "build", Status: "pending"})
	if err == nil {
		t.Fatal("re-creating the same node reported no duplicate")
	}
	if errors.Is(err, store.ErrComputeLimit) {
		t.Fatalf("re-creating the same node met a guard: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Fatalf("re-creating the same node failed with %v, want a unique-constraint violation", err)
	}
}

func TestRunsPerHourGuardRefusesAMeteredPrincipalPastTheCap(t *testing.T) {
	s := storetest.Open(t)
	claimant := meteredClaimant(t, s, "agent:cloud")
	other := meteredClaimant(t, s, "agent:other")
	setLimit(t, s, store.ComputeLimitRunsPerHour, 1)

	if err := createRunAs(t, s, claimant.Principal, "run-1"); err != nil {
		t.Fatalf("the first run must be allowed: %v", err)
	}
	err := createRunAs(t, s, claimant.Principal, "run-2")
	refused := limitRefusal(t, err, store.ComputeLimitRunsPerHour)
	if refused.Principal != claimant.Principal {
		t.Fatalf("refusal = %+v, want the creating principal", refused)
	}
	if err := createRunAs(t, s, other.Principal, "run-3"); err != nil {
		t.Fatalf("another principal's budget must be its own: %v", err)
	}

	// safety: the window is the hour before the new run, so a run created
	// before it no longer occupies the budget.
	rewindRunCreation(t, s, "run-1", time.Now().Add(-2*time.Hour))
	if err := createRunAs(t, s, claimant.Principal, "run-4"); err != nil {
		t.Fatalf("a run outside the window must not count: %v", err)
	}
}

func TestGlobalRunsPerHourGuardCountsEveryPrincipal(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setLimit(t, s, store.ComputeLimitGlobalRunsPerHour, 1)

	if err := s.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("the first run must be allowed: %v", err)
	}
	err := s.CreateRun(ctx, store.Run{
		ID: "run-2", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	})
	limitRefusal(t, err, store.ComputeLimitGlobalRunsPerHour)
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
	refused := limitRefusal(t, err, store.ComputeLimitConcurrentRunners)
	if refused.Principal != claimant.Principal {
		t.Fatalf("refusal = %+v, want the claiming principal", refused)
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
	limitRefusal(t, err, store.ComputeLimitGlobalRunners)

	runners, alarm, err := s.ComputeAlarmState(ctx)
	if err != nil {
		t.Fatalf("alarm state: %v", err)
	}
	if runners != 1 || alarm != 1 {
		t.Fatalf("alarm state = (%d, %d), want one runner against an alarm of one", runners, alarm)
	}
}

func TestAlarmStateCountsNothingWithoutAnAlarm(t *testing.T) {
	s := storetest.Open(t)
	runners, alarm, err := s.ComputeAlarmState(context.Background())
	if err != nil {
		t.Fatalf("alarm state: %v", err)
	}
	if runners != 0 || alarm != 0 {
		t.Fatalf("alarm state = (%d, %d), want zeros when no alarm is set", runners, alarm)
	}
}

func TestAnEmptyBalanceIsReportedBeforeAGuard(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-broke", "build")
	setLimit(t, s, store.ComputeLimitConcurrentRunners, 1)
	setLimit(t, s, store.ComputeLimitGlobalRunners, 1)

	_, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	if !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("claim on an empty ledger = %v, want ErrInsufficientCredits", err)
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
	limitRefusal(t, err, store.ComputeLimitRunSeconds)

	ceiling, over, err := s.RunExceedsWallClock(ctx, "run-long", time.Now())
	if err != nil {
		t.Fatalf("wall clock: %v", err)
	}
	if !over || ceiling != 60 {
		t.Fatalf("wall clock = (%d, %v), want (60, true)", ceiling, over)
	}
}

// safety: a read that fails must refuse the claim rather than read as "the run
// is young", which is what an unordered error check would have done.
func TestWallClockGuardReportsAFailedRead(t *testing.T) {
	s := storetest.Open(t)
	setLimit(t, s, store.ComputeLimitRunSeconds, 60)
	seedClaimedNode(t, s, "run-long", "build")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.RunExceedsWallClock(cancelled, "run-long", time.Now()); err == nil {
		t.Fatal("a cancelled read reported no error")
	}

	claimant := meteredClaimant(t, s, "agent:cloud")
	if err := s.MarkNodeReady(context.Background(), "run-long", "build"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if _, err := s.GrantCredits(context.Background(), store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(cancelled, claimant, "pod-1", time.Minute, nil); err == nil {
		t.Fatal("a claim on a cancelled context reported no error")
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

func TestOldestWaitingReadyNodeForPrincipalStaysInsideThePrincipal(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	mine := meteredClaimant(t, s, "agent:mine")
	theirs := meteredClaimant(t, s, "agent:theirs")
	for principal, runID := range map[string]string{mine.Principal: "run-mine", theirs.Principal: "run-theirs"} {
		if err := createRunAs(t, s, principal, runID); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
			t.Fatalf("create node: %v", err)
		}
		if err := s.MarkNodeReady(ctx, runID, "build"); err != nil {
			t.Fatalf("mark ready: %v", err)
		}
	}

	runID, nodeID, err := s.OldestWaitingReadyNodeForPrincipal(ctx, mine.Principal)
	if err != nil {
		t.Fatalf("oldest waiting: %v", err)
	}
	if runID != "run-mine" || nodeID != "build" {
		t.Fatalf("oldest waiting = %s/%s, want run-mine/build", runID, nodeID)
	}
	runID, _, err = s.OldestWaitingReadyNodeForPrincipal(ctx, "agent:nobody")
	if err != nil {
		t.Fatalf("oldest waiting for a stranger: %v", err)
	}
	if runID != "" {
		t.Fatalf("a principal with nothing waiting resolved %q", runID)
	}
}

func TestConcurrentClaimsNeverPassTheRunnerGuard(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	for i := range 6 {
		readyNode(t, s, fmt.Sprintf("run-%d", i), "build")
	}
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		10_000*store.MicroCreditsPerCredit, "pay_1", "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	setLimit(t, s, store.ComputeLimitConcurrentRunners, 2)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var claimed int
	for i := range 6 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n, err := s.ClaimNextReadyNode(ctx, claimant, fmt.Sprintf("pod-%d", i), time.Minute, nil)
			if err != nil && !errors.Is(err, store.ErrComputeLimit) {
				return
			}
			if n != nil {
				mu.Lock()
				claimed++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if claimed > 2 {
		t.Fatalf("concurrent claims awarded %d nodes past a cap of 2", claimed)
	}
	usage, err := s.ComputeUsage(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Runners > 2 {
		t.Fatalf("usage = %+v, want at most the cap", usage)
	}
}

func TestConcurrentNodeCreationNeverPassesThePerRunGuard(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setLimit(t, s, store.ComputeLimitGlobalNodesPerRun, 3)
	seedClaimedNode(t, s, "run-fan", "build")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.CreateNode(ctx, store.Node{
				RunID: "run-fan", NodeID: fmt.Sprintf("fan-%d", i), Status: "pending",
			})
		}(i)
	}
	wg.Wait()
	nodes, err := s.ListNodes(ctx, "run-fan")
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) > 3 {
		t.Fatalf("concurrent creation wrote %d nodes past a cap of 3", len(nodes))
	}
}

func TestConcurrentRunCreationNeverPassesTheHourlyGuard(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setLimit(t, s, store.ComputeLimitGlobalRunsPerHour, 3)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.CreateRun(ctx, store.Run{
				ID: fmt.Sprintf("run-%d", i), Pipeline: "demo", Status: "running", StartedAt: time.Now(),
			})
		}(i)
	}
	wg.Wait()
	runs, err := s.ListRuns(ctx, store.RunFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) > 3 {
		t.Fatalf("concurrent creation wrote %d runs past a cap of 3", len(runs))
	}
}

// safety: a claim by a token the operator never marked metered costs nothing,
// so the runner guards and the alarm must not see it at all.
func TestRunnerGuardsLeaveUnmeteredClaimsAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	_, tok, err := s.CreateTokenWith(ctx, "agent:local", store.TokenKindRunner,
		[]string{"nodes.claim"}, 0, time.Now(), store.TokenOptions{})
	if err != nil {
		t.Fatalf("mint a local token: %v", err)
	}
	local := store.ClaimIdentity{Principal: "agent:local", TokenPrefix: tok.Prefix}
	readyNode(t, s, "run-a", "build")
	readyNode(t, s, "run-b", "build")
	setLimit(t, s, store.ComputeLimitGlobalRunners, 1)
	setLimit(t, s, store.ComputeLimitConcurrentRunners, 1)
	setLimit(t, s, store.ComputeLimitRunnerAlarm, 1)

	for i, pod := range []string{"pod-1", "pod-2"} {
		n, err := s.ClaimNextReadyNode(ctx, local, pod, time.Minute, nil)
		if err != nil {
			t.Fatalf("claim %d on an unmetered token: %v", i, err)
		}
		if n == nil {
			t.Fatalf("claim %d awarded nothing", i)
		}
	}
	usage, err := s.ComputeUsage(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.Runners != 0 || usage.AlarmReached {
		t.Fatalf("usage = %+v, want unmetered claims counted as no cloud runners", usage)
	}
	runners, alarm, err := s.ComputeAlarmState(ctx)
	if err != nil {
		t.Fatalf("alarm state: %v", err)
	}
	if runners != 0 || alarm != 1 {
		t.Fatalf("alarm state = (%d, %d), want no runners against an alarm of one", runners, alarm)
	}
}

// safety: a trigger whose run a guard refused would be claimed by a worker and
// executed anyway, so the two rows are written together or not at all.
func TestConcurrentTriggerAdmissionLeavesNoOrphanTrigger(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setLimit(t, s, store.ComputeLimitGlobalRunsPerHour, 2)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("run-%d", i)
			now := time.Now()
			_ = s.CreateTriggerWithRun(ctx,
				store.Trigger{ID: id, Pipeline: "demo", TriggerSource: "manual", CreatedAt: now},
				store.Run{ID: id, Pipeline: "demo", Status: "pending", StartedAt: now, CreatedAt: now})
		}(i)
	}
	wg.Wait()

	triggers, err := s.ListTriggers(ctx, store.TriggerFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(triggers) > 2 {
		t.Fatalf("wrote %d triggers past a cap of 2", len(triggers))
	}
	for _, trig := range triggers {
		if _, err := s.GetRun(ctx, trig.ID); err != nil {
			t.Fatalf("trigger %s has no run: %v", trig.ID, err)
		}
	}
	claimed := 0
	for {
		trig, err := s.ClaimNextTrigger(ctx, time.Minute)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatalf("claim trigger: %v", err)
		}
		if trig == nil {
			break
		}
		if _, err := s.GetRun(ctx, trig.ID); err != nil {
			t.Fatalf("a worker claimed trigger %s with no run: %v", trig.ID, err)
		}
		claimed++
		if claimed > 2 {
			t.Fatalf("claimed %d triggers past a cap of 2", claimed)
		}
	}
	if claimed != len(triggers) {
		t.Fatalf("claimed %d of %d triggers", claimed, len(triggers))
	}
}

func TestTriggerAndItsRunAreRefusedTogether(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	setLimit(t, s, store.ComputeLimitGlobalRunsPerHour, 1)
	now := time.Now()
	if err := s.CreateTriggerWithRun(ctx,
		store.Trigger{ID: "run-1", Pipeline: "demo", TriggerSource: "manual", CreatedAt: now},
		store.Run{ID: "run-1", Pipeline: "demo", Status: "pending", StartedAt: now, CreatedAt: now}); err != nil {
		t.Fatalf("the first admission must be allowed: %v", err)
	}

	err := s.CreateTriggerWithRun(ctx,
		store.Trigger{ID: "run-2", Pipeline: "demo", TriggerSource: "manual", CreatedAt: now},
		store.Run{ID: "run-2", Pipeline: "demo", Status: "pending", StartedAt: now, CreatedAt: now})
	limitRefusal(t, err, store.ComputeLimitGlobalRunsPerHour)
	if trig, err := s.GetTrigger(ctx, "run-2"); err == nil && trig != nil {
		t.Fatal("the refused admission left a trigger behind")
	}
}
