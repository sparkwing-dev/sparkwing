package store_test

import (
	"context"
	"errors"
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
	claim, err := f.s.ClaimLaunch(ctx, launcherIdentity, launchRequest(), time.Now())
	if err != nil || claim == nil {
		t.Fatalf("launch claim: %+v %v", claim, err)
	}
	now := time.Now()
	tok, err := f.s.AuthorizeClaimToken(ctx, claim.Token, store.ClaimSensitive, now)
	if err != nil || tok.RunID != "run-token" || tok.NodeID != store.PlanNodeID || tok.Generation != claim.Generation {
		t.Fatalf("authorize = %+v %v", tok, err)
	}
	if !claim.ExpiresAt.Equal(tok.ExpiresAt) || claim.ExpiresAt.After(now.Add(time.Hour)) {
		t.Fatalf("token expiry %v, claim says %v; want at most the one-hour deadline", tok.ExpiresAt, claim.ExpiresAt)
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
