package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

var launcherIdentity = store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"}

func optInForTest(t *testing.T, _ *store.Store, team *store.Tenant, owner, name string) error {
	t.Helper()
	return team.SetRepoDispatch(context.Background(), owner, name, store.RepoDispatchController, time.Now())
}

func launchRequest() store.LaunchClaimRequest {
	return store.LaunchClaimRequest{HolderID: "launcher:pod", Lease: time.Minute, Deadline: time.Hour}
}

// safety: covers a wall-clock step back and stays under any backoff a test holds a node for.
const launchClockSlack = 250 * time.Millisecond

// safety: a host clock sync steps the wall clock back past an earlier ready_at stamp (WSL2: ~40ms every
// 30s), so a claim at a fresh time.Now() finds no ready node; claim at the latest ready_at just past now.
func launchClock(t *testing.T, s *store.Store) time.Time {
	t.Helper()
	now := time.Now()
	rows, err := s.DB().Query(`SELECT ready_at FROM nodes WHERE ready_at IS NOT NULL`)
	if err != nil {
		t.Fatalf("ready_at: %v", err)
	}
	defer func() { _ = rows.Close() }()
	clock := now
	for rows.Next() {
		var readyAt int64
		if err := rows.Scan(&readyAt); err != nil {
			t.Fatalf("ready_at: %v", err)
		}
		if ready := time.Unix(0, readyAt); ready.After(clock) && !ready.After(now.Add(launchClockSlack)) {
			clock = ready
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("ready_at: %v", err)
	}
	return clock
}

func intake(t *testing.T, tenant *store.Tenant, runID, owner, name string) {
	t.Helper()
	now := time.Now()
	if err := tenant.CreateTriggerWithRun(context.Background(), store.Trigger{
		ID: runID, Pipeline: "demo", GithubOwner: owner, GithubRepo: name, Repo: owner + "/" + name, CreatedAt: now,
	}, store.Run{
		ID: runID, Pipeline: "demo", Status: "pending", GithubOwner: owner, GithubRepo: name,
		DeclaredRepo: owner + "/" + name, CreatedAt: now, StartedAt: now,
	}); err != nil {
		t.Fatalf("intake %s: %v", runID, err)
	}
}

func TestRepoDispatch_OnlyAnOptedInRepoSkipsTheTriggerPath(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	if err := optInForTest(t, st, alpha, "Korey", "Probe"); err != nil {
		t.Fatal(err)
	}
	intake(t, alpha, "run-new", "korey", "probe")
	intake(t, alpha, "run-old", "korey", "other")

	runner := mintTeamClaimant(t, alpha, "agent:alpha-pool")
	claimed, err := st.ClaimNextTriggerFor(ctx, runner, time.Minute, nil, nil)
	if err != nil || claimed.ID != "run-old" {
		t.Fatalf("trigger claim = %+v, %v; want only the opted-out run-old", claimed, err)
	}
	if rest, err := st.ClaimNextTriggerFor(ctx, runner, time.Minute, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the opted-in run's trigger was claimable: %+v %v", rest, err)
	}
	nodes, err := st.ListNodes(ctx, "run-old")
	if err != nil || len(nodes) != 0 {
		t.Fatalf("the opted-out run got nodes at intake: %+v %v", nodes, err)
	}
	if n, err := st.ClaimNextReadyNode(ctx, runner, "holder", time.Minute, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a runner's queue claim took %+v (%v); the plan node is the launcher's", n, err)
	}
	if _, err := st.ClaimNamedNode(ctx, runner, "run-new", store.PlanNodeID, "holder", time.Minute,
		store.NamedClaimOptions{}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("a runner named the plan node: err = %v, want ErrLockHeld", err)
	}
	claim, err := st.ClaimLaunch(ctx, launcherIdentity, launchRequest(), launchClock(t, st))
	if err != nil || claim == nil {
		t.Fatalf("launch claim: %+v %v", claim, err)
	}
	if claim.Team != "alpha" || claim.RunID != "run-new" || claim.NodeID != store.PlanNodeID ||
		claim.Kind != store.ClaimTokenPlan || claim.Dispatch != store.RepoDispatchController {
		t.Fatalf("launch claim = %+v", claim)
	}
	for _, bad := range []store.RepoDispatch{"shell", ""} {
		if err := alpha.SetRepoDispatch(ctx, "korey", "probe", bad, time.Now()); !errors.Is(err, store.ErrInvalidInput) {
			t.Fatalf("dispatch %q: err = %v, want ErrInvalidInput", bad, err)
		}
	}
	if err := alpha.SetRepoDispatch(ctx, "korey", "other", store.RepoDispatchController, time.Now()); err != nil {
		t.Fatalf("opt a repository in: %v", err)
	}

	if err := alpha.SetRepoDispatch(ctx, "korey", "probe", store.RepoDispatchTrigger, time.Now()); err != nil {
		t.Fatalf("move the repository back to the trigger path: %v", err)
	}
	intake(t, alpha, "run-back", "korey", "probe")
	back, err := st.ClaimNextTriggerFor(ctx, runner, time.Minute, nil, nil)
	if err != nil || back.ID != "run-back" {
		t.Fatalf("trigger claim after the rollback = %+v, %v; want run-back", back, err)
	}
}

// A manual retry of an opted-in repository's run takes the controller path
// too, so a retry is never a way back onto the trigger path.
func TestRepoDispatch_ARetryOfAnOptedInRunIsControllerDispatched(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	if err := optInForTest(t, st, alpha, "korey", "probe"); err != nil {
		t.Fatal(err)
	}
	intake(t, alpha, "run-src", "korey", "probe")
	now := time.Now()
	if err := st.CreateRetryWithRun(ctx, "run-src", store.Trigger{
		ID: "run-retry", Pipeline: "demo", GithubOwner: "korey", GithubRepo: "probe", RetryOf: "run-src", CreatedAt: now,
	}, store.Run{
		ID: "run-retry", Pipeline: "demo", Status: "pending", GithubOwner: "korey", GithubRepo: "probe",
		RetryOf: "run-src", CreatedAt: now, StartedAt: now,
	}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	runner := mintTeamClaimant(t, alpha, "agent:alpha-pool")
	if claimed, err := st.ClaimNextTriggerFor(ctx, runner, time.Minute, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the retry's trigger was claimable: %+v %v", claimed, err)
	}
	nodes, err := alpha.ListNodes(ctx, "run-retry")
	if err != nil || len(nodes) != 1 || nodes[0].NodeID != store.PlanNodeID {
		t.Fatalf("retry nodes = %+v %v, want the plan node", nodes, err)
	}
}

// A launcher token bills whatever its metered flag says, and a team that
// cannot pay never holds the queue: seventeen of its unpaid nodes, more than
// one scan page, sit ahead of a paying team's node, which is the one claimed.
func TestClaimLaunch_BillsEveryClaimAndPagesPastUnpaidTeams(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	_, tok, err := st.CreateToken("launcher", store.TokenKindService, []string{store.LaunchScope}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Metered {
		t.Fatal("the launcher token was minted metered; the test needs the flag unset")
	}
	launcher := store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}
	unpaid, paying := teamHandle(t, st, "unpaid"), teamHandle(t, st, "paying")
	for _, team := range []*store.Tenant{unpaid, paying} {
		if err := optInForTest(t, st, team, "korey", "probe"); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 17 {
		intake(t, unpaid, fmt.Sprintf("run-unpaid-%02d", i), "korey", "probe")
	}
	if claim, err := st.ClaimLaunch(ctx, launcher, launchRequest(), time.Now()); err != nil || claim != nil {
		t.Fatalf("a team with no credits got a launch claim: %+v %v", claim, err)
	}
	intake(t, paying, "run-paying", "korey", "probe")
	if _, err := paying.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "pay_launch", CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	claim, err := st.ClaimLaunch(ctx, launcher, launchRequest(), launchClock(t, st))
	if err != nil || claim == nil || claim.Team != "paying" {
		t.Fatalf("launch claim = %+v %v, want the paying team's node behind the unpaid ones", claim, err)
	}
	balance, err := paying.CreditBalanceMicro(ctx)
	if err != nil || balance >= 100*store.MicroCreditsPerCent {
		t.Fatalf("paying balance after the claim = %d (%v); the claim reserved nothing", balance, err)
	}
}

// One poll scans a bounded number of candidates, and the next poll resumes
// where it stopped, so a paying team's node behind more unpaid nodes than one
// poll reads is still reached on a later poll, and the queue wraps afterwards.
func TestClaimLaunch_BoundsEachPollAndResumesWhereItStopped(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	_, tok, err := st.CreateToken("launcher", store.TokenKindService, []string{store.LaunchScope}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	launcher := store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}
	unpaid, paying := teamHandle(t, st, "unpaid"), teamHandle(t, st, "paying")
	for _, team := range []*store.Tenant{unpaid, paying} {
		if err := optInForTest(t, st, team, "korey", "probe"); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 70 {
		intake(t, unpaid, fmt.Sprintf("run-unpaid-%02d", i), "korey", "probe")
	}
	intake(t, paying, "run-paying", "korey", "probe")
	if _, err := paying.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "pay_launch", CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	if claim, err := st.ClaimLaunch(ctx, launcher, launchRequest(), time.Now()); err != nil || claim != nil {
		t.Fatalf("the first poll read past its budget to %+v (%v)", claim, err)
	}
	claim, err := st.ClaimLaunch(ctx, launcher, launchRequest(), launchClock(t, st))
	if err != nil || claim == nil || claim.Team != "paying" {
		t.Fatalf("the second poll = %+v %v, want the paying node it resumed to", claim, err)
	}
	intake(t, paying, "run-paying-2", "korey", "probe")
	var second *store.LaunchClaim
	for poll := 0; poll < 3 && second == nil; poll++ {
		if second, err = st.ClaimLaunch(ctx, launcher, launchRequest(), launchClock(t, st)); err != nil {
			t.Fatal(err)
		}
	}
	if second == nil || second.RunID != "run-paying-2" {
		t.Fatalf("after wrapping, polls reached %+v, want run-paying-2", second)
	}
}

// Concurrent polls share one resume point and each advances it past the pages
// it read, so four polls at once cover four budgets of the queue and reach a
// paying node behind 200 unpaid ones, which four polls from one stale point
// never would.
func TestClaimLaunch_ConcurrentPollsNeverRollTheResumePointBack(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	_, tok, err := st.CreateToken("launcher", store.TokenKindService, []string{store.LaunchScope}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	launcher := store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}
	unpaid, paying := teamHandle(t, st, "unpaid"), teamHandle(t, st, "paying")
	for _, team := range []*store.Tenant{unpaid, paying} {
		if err := optInForTest(t, st, team, "korey", "probe"); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 200 {
		intake(t, unpaid, fmt.Sprintf("run-unpaid-%03d", i), "korey", "probe")
	}
	intake(t, paying, "run-paying", "korey", "probe")
	if _, err := paying.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "pay_launch", CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	claims := make(chan *store.LaunchClaim, 4)
	at := launchClock(t, st)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, err := st.ClaimLaunch(ctx, launcher, launchRequest(), at)
			if err != nil {
				t.Errorf("poll: %v", err)
			}
			claims <- c
		}()
	}
	close(start)
	wg.Wait()
	close(claims)
	var got []string
	for c := range claims {
		if c != nil {
			got = append(got, c.RunID)
		}
	}
	if len(got) != 1 || got[0] != "run-paying" {
		t.Fatalf("four concurrent polls claimed %v, want run-paying once", got)
	}
}
