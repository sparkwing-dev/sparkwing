package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

var launcherIdentity = store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"}

func launchRequest() store.LaunchClaimRequest {
	return store.LaunchClaimRequest{HolderID: "launcher:pod", Lease: time.Minute, Deadline: time.Hour}
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
	if err := alpha.SetRepoDispatch(ctx, "Korey", "Probe", store.RepoDispatchController, time.Now()); err != nil {
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
	claim, err := st.ClaimLaunch(ctx, launcherIdentity, launchRequest(), time.Now())
	if err != nil || claim == nil {
		t.Fatalf("launch claim: %+v %v", claim, err)
	}
	if claim.Team != "alpha" || claim.RunID != "run-new" || claim.NodeID != store.PlanNodeID ||
		claim.Kind != store.ClaimTokenPlan || claim.Dispatch != store.RepoDispatchController {
		t.Fatalf("launch claim = %+v", claim)
	}
	if err := alpha.SetRepoDispatch(ctx, "korey", "probe", "shell", time.Now()); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("unknown dispatch: err = %v", err)
	}
}

// The token the launcher gets is the claim's own: it answers for that one node
// at that generation and ends when the lease does.
func TestClaimLaunch_MintsATokenBoundToTheClaim(t *testing.T) {
	ctx := context.Background()
	f := newDispatchRun(t, "run-token")
	claimedAt := time.Now()
	claim, err := f.s.ClaimLaunch(ctx, launcherIdentity, launchRequest(), claimedAt)
	if err != nil || claim == nil {
		t.Fatalf("launch claim: %+v %v", claim, err)
	}
	now := time.Now()
	tok, err := f.s.AuthorizeClaimToken(ctx, claim.Token, store.ClaimSensitive, now)
	if err != nil || tok.RunID != "run-token" || tok.NodeID != store.PlanNodeID || tok.Generation != claim.Generation {
		t.Fatalf("authorize = %+v %v", tok, err)
	}
	if claim.LifetimeSecs != 3600 || !tok.ExpiresAt.Equal(claimedAt.Add(time.Hour)) {
		t.Fatalf("token expiry %v, claim lifetime %ds; want one hour from the claim", tok.ExpiresAt, claim.LifetimeSecs)
	}
	if again, err := f.s.ClaimLaunch(ctx, launcherIdentity, launchRequest(), time.Now()); err != nil || again != nil {
		t.Fatalf("a held node was claimed twice: %+v %v", again, err)
	}
	if _, err := f.s.AuthorizeClaimToken(ctx, claim.Token, store.ClaimSensitive, now.Add(2*time.Minute)); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("after the lease lapsed: err = %v, want ErrClaimNotLive", err)
	}
	for _, bad := range []time.Duration{0, store.MaxClaimTokenLifetime + time.Second} {
		req := launchRequest()
		req.Deadline = bad
		if _, err := f.s.ClaimLaunch(ctx, launcherIdentity, req, time.Now()); !errors.Is(err, store.ErrInvalidInput) {
			t.Fatalf("deadline %s: err = %v", bad, err)
		}
	}
}

// A retry waits out its backoff: the launcher must not claim a node whose
// ready time is still ahead, and a node that needs executor labels is not a
// Job's to run.
func TestClaimLaunch_HonorsReadyAtAndLabels(t *testing.T) {
	ctx := context.Background()
	f := newDispatchRun(t, "run-ready")
	f.mustAccept(t, planOf(`a|"modifiers":{"retry":1,"retry_auto":true,"retry_backoff_ms":60000}`, `gpu|"modifiers":{"runs_on":["gpu"]}`))
	f.mustReport(t, "a", "failed")
	if claim, err := f.s.ClaimLaunch(ctx, launcherIdentity, launchRequest(), time.Now()); err != nil || claim != nil {
		t.Fatalf("claimed during backoff or a labeled node: %+v %v", claim, err)
	}
	later, err := f.s.ClaimLaunch(ctx, launcherIdentity, launchRequest(), time.Now().Add(2*time.Minute))
	if err != nil || later == nil || later.NodeID != "a" || later.Kind != store.ClaimTokenWork {
		t.Fatalf("after the backoff: %+v %v", later, err)
	}
}

// A sealed node of a controller-dispatched run is the launcher's even when an
// enrolled executor's offer scan or its named offer reaches it.
func TestExecutorOffer_RefusesAControllerDispatchedNode(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	if err := acme.CreateRun(ctx, store.Run{ID: "run-ctl", Pipeline: "release", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	seedSealedNode(t, st, "run-ctl", "candidate")
	if _, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `UPDATE nodes SET kind = 'work' WHERE run_id = ?`), "run-ctl"); err != nil {
		t.Fatal(err)
	}
	_, tok, err := acme.CreateToken(ctx, "agent:acme-helper", store.TokenKindRunner, []string{"nodes.claim"}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}
	if err := st.EnrollExecutor(ctx, tok.Prefix, store.Executor{
		Name: "acme-helper", Kind: "agent", Location: "local", Principal: tok.Principal,
		BasePriority: 100, PriorityCeiling: 100, MaxConcurrent: 1,
		Budget: store.ExecutorResource{Cores: 4, MemoryBytes: 4 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	reportCtx, err := executionpolicy.WithRuntimeReport(ctx, executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v0.41.0", GOOS: "linux", GOARCH: "amd64",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.HeartbeatExecutor(reportCtx, claimant, "acme-helper", store.ExecutorResource{Cores: 4, MemoryBytes: 4 << 30}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	sink := executionpolicy.NewPreparationSink()
	if _, err := st.PrepareNextExecutorClaim(executionpolicy.WithPreparationSink(ctx, sink), claimant, "acme-helper"); err != nil &&
		!errors.Is(err, executionpolicy.ErrBodyAttestationRequired) && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("PrepareNextExecutorClaim: %v", err)
	}
	if binding := sink.Load(); binding.RunID != "" {
		t.Fatalf("the offer scan prepared %s/%s, a controller-dispatched node", binding.RunID, binding.NodeID)
	}
	summary, err := st.SchedulingSummary(ctx, "run-ctl", "candidate")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := st.OfferExecutorClaim(ctx, claimant, store.ExecutorClaimOffer{
		ExecutorName: "acme-helper", HolderID: "holder", RunID: "run-ctl", NodeID: "candidate",
		ReservationID: "reservation", ResourceDigest: summary.ResourceDigest, Slot: 0,
	}); err == nil && res.Node != nil {
		t.Fatalf("an executor's named offer claimed a controller-dispatched node: %+v", res.Node)
	}
}

// A manual retry of an opted-in repository's run takes the controller path
// too, so a retry is never a way back onto the trigger path.
func TestRepoDispatch_ARetryOfAnOptedInRunIsControllerDispatched(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	if err := alpha.SetRepoDispatch(ctx, "korey", "probe", store.RepoDispatchController, time.Now()); err != nil {
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
	nodes, err := st.ListNodes(ctx, "run-retry")
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
		if err := team.SetRepoDispatch(ctx, "korey", "probe", store.RepoDispatchController, time.Now()); err != nil {
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
	if _, err := paying.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent, "pay_launch", "admin"); err != nil {
		t.Fatal(err)
	}
	claim, err := st.ClaimLaunch(ctx, launcher, launchRequest(), time.Now())
	if err != nil || claim == nil || claim.Team != "paying" {
		t.Fatalf("launch claim = %+v %v, want the paying team's node behind the unpaid ones", claim, err)
	}
	balance, err := paying.CreditBalanceMicro(ctx)
	if err != nil || balance >= 100*store.MicroCreditsPerCent {
		t.Fatalf("paying balance after the claim = %d (%v); the claim reserved nothing", balance, err)
	}
}

// The claim token, and so the Job, lives the node's declared timeout plus the
// slack, never the launcher's whole cap, and the timeout never extends it.
func TestClaimLaunch_ExpiryFollowsTheNodesTimeout(t *testing.T) {
	ctx := context.Background()
	f := newDispatchRun(t, "run-timeout")
	f.mustAccept(t, planOf(`short|"modifiers":{"timeout_ms":60000}`, `long|"modifiers":{"timeout_ms":86400000}`,
		`huge|"modifiers":{"timeout_ms":288230376151771744}`, "none"))
	now := time.Now()
	for node, want := range map[string]time.Duration{
		"short": time.Minute + store.LaunchDeadlineSlack, "long": time.Hour, "huge": time.Hour, "none": time.Hour,
	} {
		req := launchRequest()
		req.RunID, req.NodeID = f.run, node
		c, err := f.s.ClaimLaunch(ctx, launcherIdentity, req, now)
		if err != nil || c == nil {
			t.Fatalf("%s: %+v %v", node, c, err)
		}
		if got := time.Duration(c.LifetimeSecs) * time.Second; got != want {
			t.Errorf("%s: token lives %s, want %s", node, got, want)
		}
	}
}

// The launch award takes its run's row lock before the node's, the order
// settle and plan accept take, so it waits behind a holder of the run row
// rather than taking the node out from under it.
func TestClaimLaunch_WaitsForTheRunRowLock(t *testing.T) {
	f := newDispatchRun(t, "run-lock")
	if f.s.Dialect() != store.DialectPostgres {
		t.Skip("row locks are Postgres's; SQLite serializes every writer")
	}
	holder, err := f.s.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback() }()
	if _, err := holder.Exec(`SELECT id FROM runs WHERE id = $1 FOR NO KEY UPDATE`, f.run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if claim, err := f.s.ClaimLaunch(ctx, launcherIdentity, launchRequest(), time.Now()); err == nil {
		t.Fatalf("the launch claim took %+v while another transaction held its run row", claim)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	if claim, err := f.s.ClaimLaunch(context.Background(), launcherIdentity, launchRequest(), time.Now()); err != nil || claim == nil {
		t.Fatalf("after the holder released the run: %+v %v", claim, err)
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
		if err := team.SetRepoDispatch(ctx, "korey", "probe", store.RepoDispatchController, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 70 {
		intake(t, unpaid, fmt.Sprintf("run-unpaid-%02d", i), "korey", "probe")
	}
	intake(t, paying, "run-paying", "korey", "probe")
	if _, err := paying.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent, "pay_launch", "admin"); err != nil {
		t.Fatal(err)
	}
	if claim, err := st.ClaimLaunch(ctx, launcher, launchRequest(), time.Now()); err != nil || claim != nil {
		t.Fatalf("the first poll read past its budget to %+v (%v)", claim, err)
	}
	claim, err := st.ClaimLaunch(ctx, launcher, launchRequest(), time.Now())
	if err != nil || claim == nil || claim.Team != "paying" {
		t.Fatalf("the second poll = %+v %v, want the paying node it resumed to", claim, err)
	}
	intake(t, paying, "run-paying-2", "korey", "probe")
	var second *store.LaunchClaim
	for poll := 0; poll < 3 && second == nil; poll++ {
		if second, err = st.ClaimLaunch(ctx, launcher, launchRequest(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if second == nil || second.RunID != "run-paying-2" {
		t.Fatalf("after wrapping, polls reached %+v, want run-paying-2", second)
	}
}
