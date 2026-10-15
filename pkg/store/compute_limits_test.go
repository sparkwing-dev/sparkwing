package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"

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
		store.ComputeLimitGlobalRunners: 7,
		store.ComputeLimitRunSeconds:    10,
	})
	if err != nil {
		t.Fatalf("set limits: %v", err)
	}
	if limits.GlobalRunners != 7 || limits.RunSeconds != 10 {
		t.Fatalf("limits = %+v", limits)
	}
	if _, err := s.SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners: 8,
		store.ComputeLimitRunSeconds:    -1,
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
	if _, err := s.DB().Exec(`CREATE TRIGGER refuse_scale_step BEFORE INSERT ON settings
WHEN NEW.key = 'compute_limit_max_run_seconds'
BEGIN SELECT RAISE(ABORT, 'run seconds refused'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if _, err := s.SetComputeLimits(ctx, map[string]int64{
		store.ComputeLimitGlobalRunners: 7,
		store.ComputeLimitRunSeconds:    10,
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

	tn, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	runID, nodeID, err := tn.OldestWaitingReadyNodeForPrincipal(ctx, mine.Principal)
	if err != nil {
		t.Fatalf("oldest waiting: %v", err)
	}
	if runID != "run-mine" || nodeID != "build" {
		t.Fatalf("oldest waiting = %s/%s, want run-mine/build", runID, nodeID)
	}
	runID, _, err = tn.OldestWaitingReadyNodeForPrincipal(ctx, "agent:nobody")
	if err != nil {
		t.Fatalf("oldest waiting for a stranger: %v", err)
	}
	if runID != "" {
		t.Fatalf("a principal with nothing waiting resolved %q", runID)
	}
}

// A runner's principal name is unique only within its team, so another team's
// runner of the same name waiting longer is not where a refusal is recorded.
func TestOldestWaitingReadyNodeForPrincipalStaysInsideTheTeam(t *testing.T) {
	s := storetest.Open(t)
	ctx := store.WithCreatingPrincipal(context.Background(), "agent:ci")
	acme := tenantFor(t, s, "acme")
	other := tenantFor(t, s, "other")
	for _, run := range []struct {
		tn *store.Tenant
		id string
	}{{other, "run-other"}, {acme, "run-acme"}} {
		if err := run.tn.CreateRun(ctx, store.Run{ID: run.id, Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateNode(ctx, store.Node{RunID: run.id, NodeID: "build", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkNodeReady(ctx, run.id, "build"); err != nil {
			t.Fatal(err)
		}
	}
	runID, _, err := acme.OldestWaitingReadyNodeForPrincipal(ctx, "agent:ci")
	if err != nil {
		t.Fatal(err)
	}
	if runID != "run-acme" {
		t.Fatalf("acme's agent:ci resolved %q, want its own run-acme over the other team's older one", runID)
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
	if trig, err := teststore.ForTeam(t, s, store.DefaultTeam).GetTrigger(ctx, "run-2"); err == nil && trig != nil {
		t.Fatal("the refused admission left a trigger behind")
	}
}
