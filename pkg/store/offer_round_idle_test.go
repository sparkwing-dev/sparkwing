package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func runnerToken(t *testing.T, tn *store.Tenant, name string) string {
	t.Helper()
	_, tok, err := tn.CreateToken(context.Background(), "runner:"+name, store.TokenKindRunner,
		[]string{"nodes.claim"}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok.Prefix
}

func reopenRound(ctx context.Context, t *testing.T, st *store.Store, runID, nodeID string) bool {
	t.Helper()
	if revoked, err := st.RevokeNodeReady(ctx, runID, nodeID); err != nil || !revoked {
		t.Fatalf("reset offer round = %v, %v", revoked, err)
	}
	if err := st.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
	result, err := st.FinalizeExecutorClaimRound(ctx, runID, nodeID)
	if err != nil {
		t.Fatalf("FinalizeExecutorClaimRound: %v", err)
	}
	if !result.Pending && !result.Revoked {
		t.Fatalf("round result = %+v, want pending or revoked", result)
	}
	return result.Pending
}

func TestOfferRoundKeepsItsWindowOnlyWhileSomethingCouldClaim(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	home, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	acme := tenantFor(t, st, "acme")
	homeRunner := runnerToken(t, home, "home")
	acmeRunner := runnerToken(t, acme, "acme")
	seedExecutorNode(t, st, "run", 1, "linux")

	known := func(live ...store.RunnerPresence) context.Context { return store.WithQueueRunners(ctx, live) }
	for _, tc := range []struct {
		name  string
		ready context.Context
		want  bool
	}{
		{"a controller that cannot vouch for its registry", ctx, true},
		{"a controller that knows no runner is live", known(), false},
		{"a live runner of the team", known(store.RunnerPresence{Name: "home", TokenPrefix: homeRunner, Labels: []string{"linux"}}), true},
		{"a runner of the team without the label", known(store.RunnerPresence{Name: "home", TokenPrefix: homeRunner}), false},
		{"only another team's runner", known(store.RunnerPresence{Name: "acme", TokenPrefix: acmeRunner, Labels: []string{"linux"}}), false},
	} {
		if got := reopenRound(tc.ready, t, st, "run", "work"); got != tc.want {
			t.Errorf("%s: round open = %v, want %v", tc.name, got, tc.want)
		}
	}

	enrollTeamOfferExecutor(t, st, home, "home-desk", 10)
	if !reopenRound(known(), t, st, "run", "work") {
		t.Fatal("a live executor of the team: round closed at once, want the window")
	}
}

func TestOfferRoundWaitsOnlyForALiveCredentialOfTheRunsRepository(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	idle := store.WithQueueRunners(ctx, nil)
	githubWork(t, st, acme, "run-gh", "acme/wid_gets", nil)

	if reopenRound(idle, t, st, "run-gh", "compile") {
		t.Fatal("no credential for the push: round kept its window, want it closed")
	}
	binding, err := acme.AddGitHubRunnerBinding(ctx, store.GitHubRunnerBinding{
		RepositoryID: 42, RepositoryOwnerID: 7, Repository: "acme/wid_gets",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mint := func(principal string) {
		t.Helper()
		if _, _, err := acme.MintGitHubRunnerCredential(ctx, binding, principal, mainPush,
			[]string{"nodes.claim"}, time.Hour, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	mint("github:42:acme/widXgets")
	if reopenRound(idle, t, st, "run-gh", "compile") {
		t.Fatal("a credential for another repository at the commit: round kept its window, want it closed")
	}
	mint("github:42:Acme/Wid_Gets")
	if !reopenRound(idle, t, st, "run-gh", "compile") {
		t.Fatal("a live credential for the push: round closed at once, want the window")
	}
}

func TestOfferRoundIgnoresARunnerWhoseAllowListRefusesTheRepository(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	prefix := runnerToken(t, acme, "desk")
	githubWork(t, st, acme, "run-gh", "acme/widgets", nil)
	runner := func(patterns ...string) context.Context {
		live := store.RunnerPresence{Name: "desk", TokenPrefix: prefix}
		if len(patterns) > 0 {
			allow, err := sourceurl.ParseRepoAllowlist(patterns)
			if err != nil {
				t.Fatal(err)
			}
			live.Profile.Accept = allow
		}
		return store.WithQueueRunners(ctx, []store.RunnerPresence{live})
	}
	for _, tc := range []struct {
		name  string
		ready context.Context
		want  bool
	}{
		{"an allow-list naming another repository", runner("github.com/acme/other"), false},
		{"an allow-list naming the run's repository", runner("github.com/acme/widgets"), true},
		{"no allow-list", runner(), true},
	} {
		if got := reopenRound(tc.ready, t, st, "run-gh", "compile"); got != tc.want {
			t.Errorf("%s: round open = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// An executor of another team can never take this team's node, so it does
// not push the avoided executor off the node it last lost.
func TestSoftAvoidanceIgnoresAnotherTeamsExecutor(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	home, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	acme := tenantFor(t, st, "acme")
	coordinatorID, err := st.CoordinatorID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	avoided := enrollTeamOfferExecutor(t, st, home, "desk-a", 50)
	enrollTeamOfferExecutor(t, st, acme, "desk-b", 50)
	var avoidedID string
	if err := st.DB().QueryRowContext(ctx, `SELECT executor_id FROM executors WHERE name = 'desk-a'`).Scan(&avoidedID); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Minute)
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-avoid", Pipeline: "release", Status: "running", StartedAt: time.Now(),
		RetryAvoidCoordinatorID: coordinatorID, RetryAvoidExecutorKind: "agent",
		RetryAvoidExecutorID: avoidedID, RetryAvoidUntil: &until,
	}); err != nil {
		t.Fatal(err)
	}
	seedSealedNode(t, st, "run-avoid", "build")
	reportCtx, err := executionpolicy.WithRuntimeReport(ctx, executionpolicy.CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v0.41.0", GOOS: "linux", GOARCH: "amd64",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.HeartbeatExecutor(reportCtx, avoided, "desk-a",
		store.ExecutorResource{Cores: 8, MemoryBytes: 16 << 30}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	sink := executionpolicy.NewPreparationSink()
	if _, err := st.PrepareNextExecutorClaim(executionpolicy.WithPreparationSink(ctx, sink), avoided, "desk-a"); err != nil &&
		!errors.Is(err, executionpolicy.ErrBodyAttestationRequired) {
		t.Fatalf("the team's only executor should be prepared for the node it avoided: %v", err)
	}
	if binding := sink.Load(); binding.RunID != "run-avoid" {
		t.Fatalf("prepared %s/%s, want run-avoid/build", binding.RunID, binding.NodeID)
	}
}
