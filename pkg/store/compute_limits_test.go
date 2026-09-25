package store_test

import (
	"context"
	"errors"
	"fmt"
	"math"
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

func TestSetComputeLimitsWritesEveryNamedGuardOrNone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	limits, err := s.SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners:          7,
		store.ComputeLimitRunnerScaleStepCredits: 10,
	})
	if err != nil {
		t.Fatalf("set limits: %v", err)
	}
	if limits.GlobalRunners != 7 || limits.RunnerScaleStepCredits != 10 {
		t.Fatalf("limits = %+v", limits)
	}
	if _, err := s.SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners:          8,
		store.ComputeLimitRunnerScaleStepCredits: math.MaxInt64,
	}); !errors.Is(err, store.ErrInvalidComputeLimitSetting) {
		t.Fatalf("mixed update = %v, want invalid setting", err)
	}
	limits, err = s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("read limits: %v", err)
	}
	if limits.GlobalRunners != 7 {
		t.Fatalf("the good half of a refused update set the global cap to %d", limits.GlobalRunners)
	}
}

func TestSetComputeLimitsRollsBackWhenOneGuardWriteFails(t *testing.T) {
	s := storetest.OpenSQLite(t)
	ctx := context.Background()
	if _, err := s.DB().Exec(`CREATE TRIGGER refuse_scale_step BEFORE INSERT ON sparkwing_meta
WHEN NEW.key = 'compute_limit_runner_scale_step_credits'
BEGIN SELECT RAISE(ABORT, 'scale step refused'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if _, err := s.SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners:          7,
		store.ComputeLimitRunnerScaleStepCredits: 10,
	}); err == nil {
		t.Fatal("the rejected compute settings write succeeded")
	}
	limits, err := s.ComputeLimits(ctx)
	if err != nil {
		t.Fatalf("read limits: %v", err)
	}
	if limits.Any() {
		t.Fatalf("the failed transaction changed limits: %+v", limits)
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

func TestPerPrincipalGuardsUseTheRunsTeamForMetering(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	globex := teamHandle(t, s, "globex")
	const principal = "agent:shared"
	meteredTeamClaimant(t, acme, principal)
	if _, _, err := globex.CreateTokenWith(ctx, principal, store.TokenKindRunner,
		[]string{"nodes.claim"}, 0, time.Now(), store.TokenOptions{}); err != nil {
		t.Fatalf("mint globex's unmetered token: %v", err)
	}
	setLimit(t, s, store.ComputeLimitNodesPerRun, 1)
	setLimit(t, s, store.ComputeLimitRunsPerHour, 1)
	create := store.WithCreatingPrincipal(ctx, principal)
	if err := globex.CreateRun(create, store.Run{ID: "globex-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("create globex's first run: %v", err)
	}
	for _, nodeID := range []string{"build", "test"} {
		if err := s.CreateNode(ctx, store.Node{RunID: "globex-1", NodeID: nodeID, Status: "pending"}); err != nil {
			t.Fatalf("unmetered globex node %s: %v", nodeID, err)
		}
	}
	if err := globex.CreateRun(create, store.Run{ID: "globex-2", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("unmetered globex's second run: %v", err)
	}
}

func TestRunsPerHourGuardCountsSharedPrincipalWithinOneTeam(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	globex := teamHandle(t, s, "globex")
	const principal = "agent:shared"
	meteredTeamClaimant(t, acme, principal)
	meteredTeamClaimant(t, globex, principal)
	setLimit(t, s, store.ComputeLimitRunsPerHour, 1)
	create := store.WithCreatingPrincipal(ctx, principal)
	for _, tc := range []struct {
		team *store.Tenant
		id   string
	}{{acme, "acme-1"}, {globex, "globex-1"}} {
		if err := tc.team.CreateRun(create, store.Run{ID: tc.id, Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
			t.Fatalf("create %s: %v", tc.id, err)
		}
	}
	err := globex.CreateRun(create, store.Run{ID: "globex-2", Pipeline: "demo", Status: "running", StartedAt: time.Now()})
	refused := limitRefusal(t, err, store.ComputeLimitRunsPerHour)
	if refused.Observed != 1 {
		t.Fatalf("globex refusal counted %d runs, want its own one run", refused.Observed)
	}
	setLimit(t, s, store.ComputeLimitRunsPerHour, 0)
	setLimit(t, s, store.ComputeLimitGlobalRunsPerHour, 2)
	err = globex.CreateRun(create, store.Run{ID: "globex-2", Pipeline: "demo", Status: "running", StartedAt: time.Now()})
	limitRefusal(t, err, store.ComputeLimitGlobalRunsPerHour)
}

func TestRunsPerHourGuardChecksFirstAttributionOfPendingRun(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	setLimit(t, s, store.ComputeLimitRunsPerHour, 1)
	if err := createRunAs(t, s, claimant.Principal, "run-first"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.CreateTriggerWithRun(ctx,
		store.Trigger{ID: "run-pending", Pipeline: "demo", CreatedAt: now},
		store.Run{ID: "run-pending", Pipeline: "demo", Status: "pending", CreatedAt: now, StartedAt: now},
	); err != nil {
		t.Fatal(err)
	}
	limitRefusal(t, createRunAs(t, s, claimant.Principal, "run-pending"), store.ComputeLimitRunsPerHour)
	run, err := s.GetRun(ctx, "run-pending")
	if err != nil || run.Status != "pending" {
		t.Fatalf("refused run = (%+v, %v), want pending", run, err)
	}
	var principal string
	if err := s.DB().QueryRow(`SELECT created_principal FROM runs WHERE id = 'run-pending'`).Scan(&principal); err != nil || principal != "" {
		t.Fatalf("refused run principal = %q, %v, want empty", principal, err)
	}
	rewindRunCreation(t, s, "run-first", now.Add(-2*time.Hour))
	setLimit(t, s, store.ComputeLimitGlobalRunsPerHour, 1)
	if err := createRunAs(t, s, claimant.Principal, "run-pending"); err != nil {
		t.Fatalf("first attribution after budget clears: %v", err)
	}
	if err := createRunAs(t, s, claimant.Principal, "run-pending"); err != nil {
		t.Fatalf("same claimant repeating the create: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT created_principal FROM runs WHERE id = 'run-pending'`).Scan(&principal); err != nil || principal != claimant.Principal {
		t.Fatalf("attributed run principal = %q, %v, want %q", principal, err, claimant.Principal)
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

func TestConcurrentRunnerGuardCountsSharedPrincipalWithinOneTeam(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	globex := teamHandle(t, s, "globex")
	const principal = "agent:shared"
	acmeRunner := meteredTeamClaimant(t, acme, principal)
	globexRunner := meteredTeamClaimant(t, globex, principal)
	for _, tc := range []struct {
		team *store.Tenant
		id   string
	}{{acme, "acme-1"}, {globex, "globex-1"}, {globex, "globex-2"}} {
		if _, err := tc.team.GrantCredits(ctx, store.CreditGrantPaid,
			1000*store.MicroCreditsPerCredit, "pay_"+tc.id, "admin"); err != nil {
			t.Fatalf("fund %s: %v", tc.id, err)
		}
		readyTeamNode(t, s, tc.team, tc.id, "build")
	}
	setLimit(t, s, store.ComputeLimitConcurrentRunners, 1)
	if _, err := s.ClaimNextReadyNode(ctx, acmeRunner, "pod-a", time.Minute, nil); err != nil {
		t.Fatalf("acme's first claim: %v", err)
	}
	if _, err := s.ClaimNextReadyNode(ctx, globexRunner, "pod-g1", time.Minute, nil); err != nil {
		t.Fatalf("globex's first claim with a shared principal: %v", err)
	}
	_, err := s.ClaimNextReadyNode(ctx, globexRunner, "pod-g2", time.Minute, nil)
	refused := limitRefusal(t, err, store.ComputeLimitConcurrentRunners)
	if refused.Observed != 1 {
		t.Fatalf("globex refusal counted %d runners, want its own one runner", refused.Observed)
	}
	setLimit(t, s, store.ComputeLimitConcurrentRunners, 0)
	setLimit(t, s, store.ComputeLimitGlobalRunners, 2)
	_, err = s.ClaimNextReadyNode(ctx, globexRunner, "pod-g2", time.Minute, nil)
	limitRefusal(t, err, store.ComputeLimitGlobalRunners)
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

// safety: values are inlined because the two dialects spell bound parameters
// differently and every one here is a test-owned integer or identifier.
func rewindGrant(t *testing.T, s *store.Store, id string, at time.Time) {
	t.Helper()
	if _, err := s.DB().Exec(fmt.Sprintf(
		`UPDATE credit_grants SET created_at = %d WHERE id = '%s'`, at.UnixNano(), id)); err != nil {
		t.Fatalf("rewind the grant: %v", err)
	}
}

func seedRefundCharge(t *testing.T, s *store.Store, id string, credits int64, at time.Time) {
	t.Helper()
	if _, err := s.DB().Exec(fmt.Sprintf(
		`INSERT INTO credit_charges (id, run_id, node_id, token_prefix, kind, seconds, amount_micro, charged_at)
		 VALUES ('%s', 'run-x', 'build', 'pfx', '%s', -60, %d, %d)`,
		id, store.CreditChargeRefund, -credits*store.MicroCreditsPerCredit, at.UnixNano())); err != nil {
		t.Fatalf("seed a refund charge: %v", err)
	}
}

// safety: a guard an older binary stored outside its bound must still derive a
// cap, so the test writes the raw setting the way that binary would have.
func forceLimit(t *testing.T, s *store.Store, name string, value int64) {
	t.Helper()
	if _, err := s.DB().Exec(fmt.Sprintf(
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES ('compute_limit_%s', '%d', %d)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		name, value, time.Now().UnixNano())); err != nil {
		t.Fatalf("force %s: %v", name, err)
	}
}

func grantCredits(t *testing.T, s *store.Store, kind string, credits int64, reference string) store.CreditGrant {
	t.Helper()
	res, err := s.RecordCreditGrant(context.Background(), store.CreditGrantRequest{
		Kind: kind, AmountMicro: credits * store.MicroCreditsPerCredit,
		Reference: reference, CreatedBy: "admin",
	})
	if err != nil {
		t.Fatalf("grant %s: %v", kind, err)
	}
	return res.Grant
}

func reversePayment(t *testing.T, s *store.Store, credits int64, reference, reverses string) {
	t.Helper()
	if _, err := s.RecordCreditGrant(context.Background(), store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -credits * store.MicroCreditsPerCredit,
		Reference: reference, Reverses: reverses, CreatedBy: "admin",
	}); err != nil {
		t.Fatalf("reverse %s: %v", reverses, err)
	}
}

func runnerCap(t *testing.T, s *store.Store) store.RunnerCap {
	t.Helper()
	derived, err := s.RunnerCapFor(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("runner cap: %v", err)
	}
	return derived
}

func scaleTo(t *testing.T, s *store.Store, base, stepCredits int64) {
	t.Helper()
	setLimit(t, s, store.ComputeLimitConcurrentRunners, base)
	setLimit(t, s, store.ComputeLimitRunnerScaleStepCredits, stepCredits)
}

func TestRunnerCapIsUnsetUntilThePerPrincipalGuardIs(t *testing.T) {
	s := storetest.Open(t)
	setLimit(t, s, store.ComputeLimitRunnerScaleStepCredits, 5000)
	grantCredits(t, s, store.CreditGrantPaid, 15000, "pay_1")

	if derived := runnerCap(t, s); derived.Cap != 0 {
		t.Fatalf("cap = %d with max_concurrent_runners unset, want 0", derived.Cap)
	}
}

func TestRunnerCapIsTheBaseWithoutPaidGrants(t *testing.T) {
	s := storetest.Open(t)
	scaleTo(t, s, 100, 5000)
	grantCredits(t, s, store.CreditGrantFree, 10000, "welcome")

	derived := runnerCap(t, s)
	if derived.Cap != 100 || derived.RecentPaidMicro != 0 {
		t.Fatalf("cap = %+v, want the base of 100 from no paid credit", derived)
	}
}

func TestRunnerCapAddsOneBasePerPaidStep(t *testing.T) {
	s := storetest.Open(t)
	scaleTo(t, s, 100, 5000)
	grantCredits(t, s, store.CreditGrantPaid, 15000, "pay_1")

	derived := runnerCap(t, s)
	if derived.Cap != 400 {
		t.Fatalf("cap = %d after loading 15000 credits at 5000 a step, want 400", derived.Cap)
	}
	if derived.RecentPaidMicro != 15000*store.MicroCreditsPerCredit {
		t.Fatalf("recent paid = %d, want the whole payment", derived.RecentPaidMicro)
	}
}

func TestRunnerCapCountsOnlyPaidGrantsInsideTheWindow(t *testing.T) {
	s := storetest.Open(t)
	scaleTo(t, s, 100, 5000)
	grantCredits(t, s, store.CreditGrantPaid, 15000, "pay_1")
	old := grantCredits(t, s, store.CreditGrantPaid, 50000, "pay_old")
	rewindGrant(t, s, old.ID, time.Now().Add(-store.RunnerScaleWindow-time.Hour))
	seedRefundCharge(t, s, "charge_refund", 4000, time.Now())

	derived := runnerCap(t, s)
	if derived.Cap != 400 {
		t.Fatalf("cap = %d, want 400: a charge refund and a grant past the window earn nothing", derived.Cap)
	}
}

func TestARefundTakesBackTheCapItsPaymentBought(t *testing.T) {
	s := storetest.Open(t)
	scaleTo(t, s, 100, 5000)
	grantCredits(t, s, store.CreditGrantPaid, 15000, "pay_1")
	if derived := runnerCap(t, s); derived.Cap != 400 {
		t.Fatalf("cap = %d before the refund, want 400", derived.Cap)
	}

	reversePayment(t, s, 10000, "refund_1", "pay_1")
	derived := runnerCap(t, s)
	if derived.Cap != 200 {
		t.Fatalf("cap = %d after 10000 of 15000 credits came back, want 200", derived.Cap)
	}
	if derived.RecentPaidMicro != 5000*store.MicroCreditsPerCredit {
		t.Fatalf("recent paid = %d, want the payment less the refund", derived.RecentPaidMicro)
	}
}

func TestARefundIsMatchedToItsPaymentRatherThanItsOwnDate(t *testing.T) {
	s := storetest.Open(t)
	scaleTo(t, s, 100, 5000)

	// safety: the reversal is dated before the window to prove the match runs
	// off the payment it names rather than off its own stamp.
	grantCredits(t, s, store.CreditGrantPaid, 15000, "pay_1")
	reversePayment(t, s, 15000, "refund_1", "pay_1")
	reversal, err := s.ListCreditGrants(context.Background(), 10)
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	for _, g := range reversal {
		if g.Kind == store.CreditGrantReversal {
			rewindGrant(t, s, g.ID, time.Now().Add(-store.RunnerScaleWindow-time.Hour))
		}
	}
	if derived := runnerCap(t, s); derived.Cap != 100 {
		t.Fatalf("cap = %d, want 100: an aged refund still takes back the payment", derived.Cap)
	}

	// safety: refunding an aged-out payment must not spend a fresh payment's
	// allowance, which a net-over-the-window sum would let it do.
	old := grantCredits(t, s, store.CreditGrantPaid, 50000, "pay_old")
	rewindGrant(t, s, old.ID, time.Now().Add(-store.RunnerScaleWindow-time.Hour))
	grantCredits(t, s, store.CreditGrantPaid, 10000, "pay_2")
	reversePayment(t, s, 50000, "refund_old", "pay_old")
	if derived := runnerCap(t, s); derived.Cap != 300 {
		t.Fatalf("cap = %d, want 300: refunding an aged payment leaves this month's alone", derived.Cap)
	}
}

func TestRunnerCapHoldsUnderTheCeiling(t *testing.T) {
	s := storetest.Open(t)
	scaleTo(t, s, 100, 5000)
	setLimit(t, s, store.ComputeLimitGlobalRunners, 300)
	grantCredits(t, s, store.CreditGrantPaid, 15000, "pay_1")

	if derived := runnerCap(t, s); derived.Cap != 300 {
		t.Fatalf("cap = %d, want the global ceiling of 300", derived.Cap)
	}
	setLimit(t, s, store.ComputeLimitRunnerScaleCeiling, 250)
	if derived := runnerCap(t, s); derived.Cap != 250 {
		t.Fatalf("cap = %d, want the scale ceiling of 250", derived.Cap)
	}
}

func TestAScaledCapNeverFallsBelowTheStaticGuard(t *testing.T) {
	s := storetest.Open(t)
	scaleTo(t, s, 100, 5000)
	setLimit(t, s, store.ComputeLimitRunnerScaleCeiling, 10)

	if derived := runnerCap(t, s); derived.Cap != 100 {
		t.Fatalf("cap = %d under a ceiling of 10, want max_concurrent_runners of 100", derived.Cap)
	}
}

func TestRunnerScaleBaseReplacesTheStaticCap(t *testing.T) {
	s := storetest.Open(t)
	scaleTo(t, s, 10, 5000)
	setLimit(t, s, store.ComputeLimitRunnerScaleBase, 100)
	grantCredits(t, s, store.CreditGrantPaid, 5000, "pay_1")

	if derived := runnerCap(t, s); derived.Cap != 200 {
		t.Fatalf("cap = %d, want the scale base of 100 doubled by one step", derived.Cap)
	}
}

func TestScalingGuardsRefuseAValuePastTheirBound(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	for name, most := range map[string]int64{
		store.ComputeLimitRunnerScaleBase:        store.RunnerScaleMaxRunners,
		store.ComputeLimitRunnerScaleCeiling:     store.RunnerScaleMaxRunners,
		store.ComputeLimitRunnerScaleStepCredits: store.RunnerScaleMaxStepCredits,
	} {
		if err := s.SetComputeLimit(ctx, name, most); err != nil {
			t.Fatalf("%s at its bound was refused: %v", name, err)
		}
		if err := s.SetComputeLimit(ctx, name, most+1); err == nil {
			t.Fatalf("%s accepted %d, past its bound of %d", name, most+1, most)
		}
	}
}

func TestRunnerCapSurvivesASettingPastItsBound(t *testing.T) {
	s := storetest.Open(t)
	setLimit(t, s, store.ComputeLimitConcurrentRunners, 100)
	grantCredits(t, s, store.CreditGrantPaid, 15000, "pay_1")

	// safety: 1<<58 credits is the value that overflowed the micro conversion.
	for _, step := range []int64{1 << 58, (1 << 58) + 1, math.MaxInt64} {
		forceLimit(t, s, store.ComputeLimitRunnerScaleStepCredits, step)
		derived := runnerCap(t, s)
		if derived.Cap != 100 {
			t.Fatalf("cap = %d at a step of %d, want the base of 100", derived.Cap, step)
		}
	}
}

func TestRunnerCapSaturatesAtTheCeilingOnATinyStep(t *testing.T) {
	s := storetest.Open(t)
	scaleTo(t, s, store.RunnerScaleMaxRunners, 1)
	setLimit(t, s, store.ComputeLimitRunnerScaleCeiling, store.RunnerScaleMaxRunners)
	grantCredits(t, s, store.CreditGrantPaid, 15000, "pay_1")

	if derived := runnerCap(t, s); derived.Cap != store.RunnerScaleMaxRunners {
		t.Fatalf("cap = %d, want the ceiling of %d", derived.Cap, store.RunnerScaleMaxRunners)
	}
}

func TestRunnerCapIsCachedUntilTheLedgerChanges(t *testing.T) {
	s := storetest.Open(t)
	scaleTo(t, s, 100, 5000)
	grantCredits(t, s, store.CreditGrantPaid, 5000, "pay_1")
	if derived := runnerCap(t, s); derived.Cap != 200 {
		t.Fatalf("cap = %d, want 200 from one step", derived.Cap)
	}

	reversePayment(t, s, 5000, "refund_1", "pay_1")
	if derived := runnerCap(t, s); derived.Cap != 100 {
		t.Fatalf("cap = %d after the refund, want the recomputed 100", derived.Cap)
	}

	grantCredits(t, s, store.CreditGrantPaid, 10000, "pay_2")
	if derived := runnerCap(t, s); derived.Cap != 300 {
		t.Fatalf("cap = %d after a new payment, want 300 rather than a cached 100", derived.Cap)
	}
}

func TestScaledRunnerCapRefusesTheClaimPastTheDerivedCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	for _, node := range []string{"a", "b", "c"} {
		readyNode(t, s, "run-"+node, "build")
	}
	scaleTo(t, s, 1, 5000)
	grantCredits(t, s, store.CreditGrantPaid, 5000, "pay_1")

	for _, pod := range []string{"pod-1", "pod-2"} {
		if _, err := s.ClaimNextReadyNode(ctx, claimant, pod, time.Minute, nil); err != nil {
			t.Fatalf("claim %s must fit under a scaled cap of two: %v", pod, err)
		}
	}
	_, err := s.ClaimNextReadyNode(ctx, claimant, "pod-3", time.Minute, nil)
	refused := limitRefusal(t, err, store.ComputeLimitConcurrentRunners)
	if refused.Cap != 2 || refused.Observed != 2 {
		t.Fatalf("refusal = %+v, want cap 2 observed 2", refused)
	}
}

func TestAnUnreadableLedgerRefusesWithTheGuardsOwnShape(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-a", "build")
	scaleTo(t, s, 2, 5000)
	grantCredits(t, s, store.CreditGrantPaid, 5000, "pay_1")
	// safety: dropping the column only the derivation reads leaves the balance
	// check the claim makes first intact, so the refusal under test is the one
	// the guard raises.
	if _, err := s.DB().Exec(`ALTER TABLE credit_grants DROP COLUMN reverses`); err != nil {
		t.Fatalf("drop the reversal column: %v", err)
	}

	_, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	refused := limitRefusal(t, err, store.ComputeLimitConcurrentRunners)
	if refused.Cap != 2 {
		t.Fatalf("refusal = %+v, want the static cap of 2", refused)
	}
}

func TestGlobalRunnerGuardOutranksAScaledCap(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	first := meteredClaimant(t, s, "agent:cloud-a")
	second := meteredClaimant(t, s, "agent:cloud-b")
	readyNode(t, s, "run-a", "build")
	readyNode(t, s, "run-b", "build")
	scaleTo(t, s, 1, 5000)
	setLimit(t, s, store.ComputeLimitGlobalRunners, 1)
	grantCredits(t, s, store.CreditGrantPaid, 50000, "pay_1")

	if _, err := s.ClaimNextReadyNode(ctx, first, "pod-a", time.Minute, nil); err != nil {
		t.Fatalf("the first claim must succeed: %v", err)
	}
	_, err := s.ClaimNextReadyNode(ctx, second, "pod-b", time.Minute, nil)
	limitRefusal(t, err, store.ComputeLimitGlobalRunners)
}
