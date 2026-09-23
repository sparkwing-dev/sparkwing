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

func TestGitHubAppConnectStateIsUsedOnce(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	expires := now.Add(10 * time.Minute)
	if ok, err := st.ConsumeGitHubAppConnectState(ctx, "nonce-1", expires, now); err != nil || !ok {
		t.Fatalf("first use = %v, %v; want accepted", ok, err)
	}
	if ok, err := st.ConsumeGitHubAppConnectState(ctx, "nonce-1", expires, now); err != nil || ok {
		t.Fatalf("second use = %v, %v; want refused", ok, err)
	}
	if ok, err := st.ConsumeGitHubAppConnectState(ctx, "nonce-2", expires, now); err != nil || !ok {
		t.Fatalf("another state = %v, %v; want accepted", ok, err)
	}
}

func TestGitHubAppDeliveryIsRememberedAcrossTeams(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	if seen, err := st.GitHubAppDeliverySeen(ctx, "digest-1"); err != nil || seen {
		t.Fatalf("unseen delivery = %v, %v", seen, err)
	}
	if err := st.RecordGitHubAppDelivery(ctx, "digest-1", "d-1", now); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordGitHubAppDelivery(ctx, "digest-1", "d-1", now); err != nil {
		t.Fatalf("recording twice: %v", err)
	}
	if seen, err := st.GitHubAppDeliverySeen(ctx, "digest-1"); err != nil || !seen {
		t.Fatalf("recorded delivery = %v, %v; want seen", seen, err)
	}
	later := now.Add(store.GitHubAppDeliveryRetention + time.Hour)
	if err := st.RecordGitHubAppDelivery(ctx, "digest-2", "d-2", later); err != nil {
		t.Fatal(err)
	}
	if seen, _ := st.GitHubAppDeliverySeen(ctx, "digest-1"); seen {
		t.Fatal("a digest past its retention was kept")
	}
}

func TestGitHubCheckRunIsRecordedOnce(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	sha := "0123456789abcdef0123456789abcdef01234567"
	for _, trig := range []struct {
		tenant *store.Tenant
		id     string
		sha    string
	}{{acme, "run-a", sha}, {acme, "run-b", sha}, {acme, "run-c", "fedcba9876543210fedcba9876543210fedcba98"}, {other, "run-x", sha}} {
		if err := trig.tenant.CreateTrigger(ctx, store.Trigger{
			ID: trig.id, Pipeline: "build", GitSHA: trig.sha, GithubOwner: "acme", GithubRepo: "widgets", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if id, err := acme.GitHubCheckRun(ctx, "run-a"); err != nil || id != 0 {
		t.Fatalf("check run before any = %d, %v; want 0", id, err)
	}
	if id, err := acme.RecordGitHubCheckRun(ctx, "run-a", 41); err != nil || id != 41 {
		t.Fatalf("record = %d, %v", id, err)
	}
	if id, err := acme.RecordGitHubCheckRun(ctx, "run-a", 42); err != nil || id != 41 {
		t.Fatalf("second record = %d, %v; want the first kept", id, err)
	}
	if _, err := acme.GitHubCheckRun(ctx, "no-such-run"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("check run of a missing trigger = %v, want ErrNotFound", err)
	}
	if _, err := other.RecordGitHubCheckRun(ctx, "run-b", 43); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another team recording on acme's run = %v, want ErrNotFound", err)
	}
	if id, err := acme.GitHubCheckRun(ctx, "run-b"); err != nil || id != 0 {
		t.Fatalf("acme's run-b after another team's record = %d, %v; want 0", id, err)
	}

	got, err := acme.GitHubCommitTriggers(ctx, store.GitHubRepo{Owner: "acme", Name: "widgets"}, sha)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, trig := range got {
		ids = append(ids, trig.ID)
	}
	if len(ids) != 2 || ids[0] != "run-b" || ids[1] != "run-a" {
		t.Fatalf("acme's triggers for the commit = %v, want [run-b run-a] and not the other team's", ids)
	}
}
