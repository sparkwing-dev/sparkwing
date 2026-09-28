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

func runnerToken(t *testing.T, tn *store.Tenant, name string) string {
	t.Helper()
	_, tok, err := tn.CreateToken(context.Background(), "runner:"+name, store.TokenKindRunner,
		[]string{"nodes.claim"}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tok.Prefix
}

// reopenRound opens a fresh offer round for the node with live as the
// controller's queue runners and reports whether the round is still open.
func reopenRound(t *testing.T, st *store.Store, runID, nodeID string, live []store.RunnerPresence) bool {
	t.Helper()
	ctx := context.Background()
	if revoked, err := st.RevokeNodeReady(ctx, runID, nodeID); err != nil || !revoked {
		t.Fatalf("reset offer round = %v, %v", revoked, err)
	}
	if err := st.MarkNodeReady(store.WithQueueRunners(ctx, live), runID, nodeID); err != nil {
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

	for _, tc := range []struct {
		name string
		live []store.RunnerPresence
		want bool
	}{
		{"no runners reported", nil, true},
		{"a live runner of the team", []store.RunnerPresence{{Name: "home", TokenPrefix: homeRunner, Labels: []string{"linux"}}}, true},
		{"a runner of the team without the label", []store.RunnerPresence{{Name: "home", TokenPrefix: homeRunner}}, false},
		{"only another team's runner", []store.RunnerPresence{{Name: "acme", TokenPrefix: acmeRunner, Labels: []string{"linux"}}}, false},
	} {
		if got := reopenRound(t, st, "run", "work", tc.live); got != tc.want {
			t.Errorf("%s: round open = %v, want %v", tc.name, got, tc.want)
		}
	}

	enrollTeamOfferExecutor(t, st, home, "home-desk", 10)
	if !reopenRound(t, st, "run", "work", []store.RunnerPresence{{Name: "acme", TokenPrefix: acmeRunner, Labels: []string{"linux"}}}) {
		t.Fatal("a live executor of the team: round closed at once, want the window")
	}
}

func TestOfferRoundWaitsForALiveGitHubActionsCredential(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	home, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	acme := tenantFor(t, st, "acme")
	elsewhere := []store.RunnerPresence{{Name: "home", TokenPrefix: runnerToken(t, home, "home")}}
	githubWork(t, st, acme, "run-gh", "acme/widgets", nil)

	if reopenRound(t, st, "run-gh", "compile", elsewhere) {
		t.Fatal("no credential for the push: round kept its window, want it closed")
	}
	binding, err := acme.AddGitHubRunnerBinding(ctx, store.GitHubRunnerBinding{
		RepositoryID: 42, RepositoryOwnerID: 7, Repository: "acme/widgets",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acme.MintGitHubRunnerCredential(ctx, binding, "github:42:acme/widgets", mainPush,
		[]string{"nodes.claim"}, time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !reopenRound(t, st, "run-gh", "compile", elsewhere) {
		t.Fatal("a live credential for the push: round closed at once, want the window")
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
