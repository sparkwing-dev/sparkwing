package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func acmeInstallation() store.GitHubAppInstallation {
	return store.GitHubAppInstallation{
		InstallationID: 7, AccountID: 70, AccountLogin: "acme", AccountType: "Organization",
		ConnectedBy: "acct-1", GitHubUserID: 501,
	}
}

func TestGitHubAppInstallationBelongsToOneTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	now := time.Now()

	if _, err := acme.BindGitHubAppInstallation(ctx, acmeInstallation(), now); err != nil {
		t.Fatalf("bind: %v", err)
	}
	stolen := acmeInstallation()
	stolen.ConnectedBy = "acct-evil"
	if _, err := other.BindGitHubAppInstallation(ctx, stolen, now); !errors.Is(err, store.ErrInstallationBoundElsewhere) {
		t.Fatalf("second team's bind = %v, want ErrInstallationBoundElsewhere", err)
	}
	got, err := st.AsOperator().GitHubAppInstallationTeam(ctx, 7)
	if err != nil || got.Team != "acme" || got.ConnectedBy != "acct-1" {
		t.Fatalf("binding after the refused takeover = %+v, %v; want acme's, untouched", got, err)
	}
	if _, err := other.GitHubAppInstallation(ctx, 7); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the other team reads acme's binding: %v", err)
	}

	again := acmeInstallation()
	again.ConnectedBy = "acct-2"
	if b, err := acme.BindGitHubAppInstallation(ctx, again, now); err != nil || b.ConnectedBy != "acct-2" {
		t.Fatalf("acme's reconnect = %+v, %v; want refreshed", b, err)
	}
}

func TestGitHubAppUnbindDropsTheTeamsSubscriptions(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	now := time.Now()
	if _, err := acme.BindGitHubAppInstallation(ctx, acmeInstallation(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.PutGitHubAppTrigger(ctx, store.GitHubAppTrigger{
		RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7, Pipeline: "build", Push: true,
	}, now); err != nil {
		t.Fatalf("put trigger: %v", err)
	}
	if subs, err := acme.GitHubAppTriggersFor(ctx, 7, 701); err != nil || len(subs) != 1 || !subs[0].Push {
		t.Fatalf("subscriptions = %+v, %v", subs, err)
	}
	team, err := st.AsOperator().UnbindGitHubAppInstallation(ctx, 7)
	if err != nil || team != "acme" {
		t.Fatalf("operator unbind = %q, %v", team, err)
	}
	if subs, err := acme.GitHubAppTriggers(ctx); err != nil || len(subs) != 0 {
		t.Fatalf("subscriptions after unbind = %+v, %v; want none", subs, err)
	}
	if _, err := acme.PutGitHubAppTrigger(ctx, store.GitHubAppTrigger{
		RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7, Pipeline: "build", Push: true,
	}, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("subscribing through an unbound installation = %v, want ErrNotFound", err)
	}
}

func untrustedWork(t *testing.T, st *store.Store, runID string, untrusted bool, parent string) {
	t.Helper()
	ctx := context.Background()
	tenant, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := tenant.CreateTriggerWithRun(ctx, store.Trigger{
		ID: runID, Pipeline: "build", CreatedAt: now, Untrusted: untrusted, ParentRunID: parent,
	}, store.Run{ID: runID, Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now, ParentRunID: parent}); err != nil {
		t.Fatalf("create %s: %v", runID, err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "compile", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, runID, "compile"); err != nil {
		t.Fatal(err)
	}
}

func TestUntrustedRunIsInheritedByChildrenAndRetries(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tenant, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	untrustedWork(t, st, "fork-run", true, "")
	untrustedWork(t, st, "child-run", false, "fork-run")
	untrustedWork(t, st, "own-run", false, "")
	now := time.Now()
	if err := st.CreateRetryWithRun(ctx, "fork-run", store.Trigger{
		ID: "retry-run", Pipeline: "build", RetryOf: "fork-run", CreatedAt: now,
	}, store.Run{ID: "retry-run", Pipeline: "build", Status: "pending", CreatedAt: now, StartedAt: now}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	for id, want := range map[string]bool{"fork-run": true, "child-run": true, "retry-run": true, "own-run": false} {
		got, err := tenant.RunUntrusted(ctx, id)
		if err != nil || got != want {
			t.Errorf("RunUntrusted(%s) = %v, %v; want %v", id, got, err, want)
		}
	}
}

func TestMeteredClaimantNeverTakesAnUntrustedRun(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	untrustedWork(t, st, "fork-run", true, "")
	pool := meteredClaimant(t, st, "agent:cloud")
	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, 1_000_000_000, "", "operator"); err != nil {
		t.Fatal(err)
	}

	if tr, err := st.ClaimNextTriggerFor(ctx, pool, 0, nil, nil); err == nil {
		t.Fatalf("metered pool claimed untrusted trigger %s", tr.ID)
	}
	if _, err := st.ClaimSpecificTriggerFor(ctx, "fork-run", pool, 0); err == nil {
		t.Fatal("metered pool claimed the untrusted trigger by id")
	}
	if n, err := st.ClaimNextReadyNode(ctx, pool, "pod-1", time.Minute, nil); err == nil && n != nil {
		t.Fatalf("metered pool claimed node %s/%s of an untrusted run", n.RunID, n.NodeID)
	}
	if _, err := st.ClaimNamedNode(ctx, pool, "fork-run", "compile", "k8s-job:1", time.Minute,
		store.NamedClaimOptions{}); !errors.Is(err, store.ErrUntrustedRun) {
		t.Fatalf("metered claim of the untrusted node by name = %v, want ErrUntrustedRun", err)
	}

	untrustedWork(t, st, "own-run", false, "")
	if tr, err := st.ClaimNextTriggerFor(ctx, pool, 0, nil, nil); err != nil || tr.ID != "own-run" {
		t.Fatalf("metered trigger claim behind an untrusted one = %+v, %v; want own-run", tr, err)
	}
	if n, err := st.ClaimNextReadyNode(ctx, pool, "pod-1", time.Minute, nil); err != nil || n == nil || n.RunID != "own-run" {
		t.Fatalf("metered claim of a trusted node = %+v, %v; want own-run", n, err)
	}
	local := unmeteredClaimant(t, st, "agent:laptop")
	if tr, err := st.ClaimSpecificTriggerFor(ctx, "fork-run", local, 0); err != nil || tr.ID != "fork-run" {
		t.Fatalf("unmetered claim of the untrusted trigger = %+v, %v", tr, err)
	}
	claimed, err := st.ClaimedRunFor(ctx, "fork-run", local, time.Now())
	if err != nil || !claimed.Untrusted {
		t.Fatalf("ClaimedRunFor(fork-run) = %+v, %v; want untrusted", claimed, err)
	}
}
