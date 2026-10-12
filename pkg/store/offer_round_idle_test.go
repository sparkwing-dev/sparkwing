package store_test

import (
	"context"
	"testing"
	"time"

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

func seedRoundNode(t *testing.T, st *store.Store, runID string, labels ...string) {
	t.Helper()
	ctx := context.Background()
	plan := []byte(`{"nodes":[{"id":"work","modifiers":{"res_cores":1}}]}`)
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "round-test", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "work", Status: "pending", NeedsLabels: labels}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.MarkNodeReady(ctx, runID, "work"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
}

func reopenRound(ctx context.Context, t *testing.T, st *store.Store, runID, nodeID string) bool {
	t.Helper()
	if revoked, err := runTeam(ctx, t, st, runID).RevokeNodeReady(ctx, runID, nodeID); err != nil || !revoked {
		t.Fatalf("reset offer round = %v, %v", revoked, err)
	}
	if err := st.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
	result, err := st.FinalizeClaimRound(ctx, runID, nodeID)
	if err != nil {
		t.Fatalf("FinalizeClaimRound: %v", err)
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
	seedRoundNode(t, st, "run", "linux")

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
