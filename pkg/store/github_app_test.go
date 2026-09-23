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

func TestGitHubAppTagSubscriptionRoundTrip(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	now := time.Now()
	if _, err := acme.BindGitHubAppInstallation(ctx, acmeInstallation(), now); err != nil {
		t.Fatal(err)
	}
	tr := store.GitHubAppTrigger{RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7, Pipeline: "release", Tags: true}
	if _, err := acme.PutGitHubAppTrigger(ctx, tr, now); err != nil {
		t.Fatal(err)
	}
	subs, err := acme.GitHubAppTriggersFor(ctx, 7, 701)
	if err != nil || len(subs) != 1 || !subs[0].Tags || subs[0].Push || subs[0].PullRequest {
		t.Fatalf("tag-only subscription = %+v, %v", subs, err)
	}
	tr.Tags, tr.Push = false, true
	if _, err := acme.PutGitHubAppTrigger(ctx, tr, now); err != nil {
		t.Fatal(err)
	}
	subs, err = acme.GitHubAppTriggers(ctx)
	if err != nil || len(subs) != 1 || subs[0].Tags || !subs[0].Push {
		t.Fatalf("replaced subscription = %+v, %v", subs, err)
	}
}

func TestGitHubAppTagSubscriptionMigrationPreservesBranchSubscription(t *testing.T) {
	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	if _, err := acme.BindGitHubAppInstallation(ctx, acmeInstallation(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.PutGitHubAppTrigger(ctx, store.GitHubAppTrigger{
		RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7, Pipeline: "build", Push: true,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`ALTER TABLE github_app_triggers DROP COLUMN on_tags`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 63`,
	} {
		if _, err := st.DB().ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	acme = teamHandle(t, st, "acme")
	subs, err := acme.GitHubAppTriggers(ctx)
	if err != nil || len(subs) != 1 || !subs[0].Push || subs[0].Tags {
		t.Fatalf("subscription after v63 migration = %+v, %v", subs, err)
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
