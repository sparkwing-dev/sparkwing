package store_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
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
	tr := store.GitHubAppTrigger{RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7, Pipeline: "release", Tags: []string{"v*"}}
	if _, err := acme.PutGitHubAppTrigger(ctx, tr, now); err != nil {
		t.Fatal(err)
	}
	subs, err := acme.GitHubAppTriggersFor(ctx, 7, 701)
	if err != nil || len(subs) != 1 || len(subs[0].Tags) != 1 || subs[0].Tags[0] != "v*" || subs[0].Push || subs[0].PullRequest {
		t.Fatalf("tag-only subscription = %+v, %v", subs, err)
	}
	tr.Tags, tr.Push = nil, true
	if _, err := acme.PutGitHubAppTrigger(ctx, tr, now); err != nil {
		t.Fatal(err)
	}
	subs, err = acme.GitHubAppTriggers(ctx)
	if err != nil || len(subs) != 1 || len(subs[0].Tags) != 0 || !subs[0].Push {
		t.Fatalf("replaced subscription = %+v, %v", subs, err)
	}
}

func TestGitHubAppSubscriptionBranchesRoundTrip(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	now := time.Now()
	if _, err := acme.BindGitHubAppInstallation(ctx, acmeInstallation(), now); err != nil {
		t.Fatal(err)
	}
	tr := store.GitHubAppTrigger{
		RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7,
		Pipeline: "deploy", Push: true, PullRequest: true,
		Branches: []string{"main", "release/*"}, BaseBranches: []string{"main"},
	}
	if _, err := acme.PutGitHubAppTrigger(ctx, tr, now); err != nil {
		t.Fatal(err)
	}
	for _, list := range []func(context.Context) ([]store.GitHubAppTrigger, error){
		acme.GitHubAppTriggers,
		func(ctx context.Context) ([]store.GitHubAppTrigger, error) {
			return acme.GitHubAppTriggersFor(ctx, 7, 701)
		},
	} {
		got, err := list(ctx)
		if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].Branches, tr.Branches) || !reflect.DeepEqual(got[0].BaseBranches, tr.BaseBranches) {
			t.Fatalf("subscription after write = %+v, %v", got, err)
		}
	}
	tr.Branches, tr.BaseBranches = nil, nil
	if _, err := acme.PutGitHubAppTrigger(ctx, tr, now); err != nil {
		t.Fatal(err)
	}
	got, err := acme.GitHubAppTriggersFor(ctx, 7, 701)
	if err != nil || len(got) != 1 || len(got[0].Branches) != 0 || len(got[0].BaseBranches) != 0 {
		t.Fatalf("subscription after clearing filters = %+v, %v", got, err)
	}
}

func TestGitHubAppTriggerOptionsRoundTripAndDefaultOff(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	now := time.Now()
	if _, err := acme.BindGitHubAppInstallation(ctx, acmeInstallation(), now); err != nil {
		t.Fatal(err)
	}
	base := store.GitHubAppTrigger{RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7, Pipeline: "base", Push: true}
	if _, err := acme.PutGitHubAppTrigger(ctx, base, now); err != nil {
		t.Fatal(err)
	}
	opt := store.GitHubAppTrigger{
		RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7, Pipeline: "opt",
		PullRequestClosed: true, PullRequestLabeled: true, PullRequestLabels: []string{"ship"}, PullRequestReadyForReview: true,
		ReleasePublished: true, ReleasePrereleased: true, BranchCreate: true, BranchDelete: true,
	}
	if _, err := acme.PutGitHubAppTrigger(ctx, opt, now); err != nil {
		t.Fatal(err)
	}
	subs, err := acme.GitHubAppTriggersFor(ctx, 7, 701)
	if err != nil || len(subs) != 2 {
		t.Fatalf("subscriptions = %+v, %v", subs, err)
	}
	if subs[0].PullRequestClosed || subs[0].ReleasePublished || subs[0].BranchDelete {
		t.Fatalf("existing event defaults = %+v", subs[0])
	}
	got := subs[1]
	if !got.PullRequestClosed || !got.PullRequestLabeled || len(got.PullRequestLabels) != 1 || got.PullRequestLabels[0] != "ship" ||
		!got.PullRequestReadyForReview || !got.ReleasePublished || !got.ReleasePrereleased || !got.BranchCreate || !got.BranchDelete {
		t.Fatalf("new event options = %+v", got)
	}
	if _, err := acme.PutGitHubAppTrigger(ctx, store.GitHubAppTrigger{
		RepositoryID: 701, Repository: "acme/widgets", InstallationID: 7,
		Pipeline: "bad", PullRequestLabeled: true,
	}, now); !errors.Is(err, store.ErrInvalidInput) {
		t.Fatalf("empty label filter = %v", err)
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

func TestGitHubAppDeliveryKeepsItsFirstEvent(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	for _, tc := range []struct {
		event string
		want  bool
	}{{"repository", true}, {"repository", true}, {"installation", false}} {
		if same, err := st.BindGitHubAppDeliveryEvent(ctx, "digest-1", tc.event, "d-1", now); err != nil || same != tc.want {
			t.Fatalf("bind as %s = %v, %v; want %v", tc.event, same, err, tc.want)
		}
	}
	if seen, err := st.GitHubAppDeliverySeen(ctx, "digest-1"); err != nil || seen {
		t.Fatalf("a bound delivery reads as processed: %v, %v", seen, err)
	}
	later := now.Add(store.GitHubAppDeliveryRetention + time.Hour)
	if same, err := st.BindGitHubAppDeliveryEvent(ctx, "digest-1", "installation", "d-1", later); err != nil || !same {
		t.Fatalf("a binding past its retention was kept without any run starting: %v, %v", same, err)
	}
}

func TestGitHubCheckRunIsRecordedOnce(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme, other := teamHandle(t, st, "acme"), teamHandle(t, st, "other")
	sha := "0123456789abcdef0123456789abcdef01234567"
	repoID := int64(4_000_000_701)
	for _, trig := range []struct {
		tenant *store.Tenant
		id     string
		sha    string
	}{{acme, "run-a", sha}, {acme, "run-b", sha}, {acme, "run-c", "fedcba9876543210fedcba9876543210fedcba98"}, {other, "run-x", sha}} {
		if err := trig.tenant.CreateTrigger(ctx, store.Trigger{
			ID: trig.id, Pipeline: "build", GitSHA: trig.sha, GithubOwner: "acme", GithubRepo: "widgets", GithubRepoID: repoID, CreatedAt: time.Now(),
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

	got, err := acme.GitHubCommitTriggers(ctx, store.GitHubRepo{ID: repoID, Owner: "acme", Name: "widgets"}, sha)
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
	got, err = acme.GitHubCommitTriggers(ctx, store.GitHubRepo{ID: repoID + 1, Owner: "acme", Name: "widgets"}, sha)
	if err != nil || len(got) != 0 {
		t.Fatalf("recreated repository inherited old runs: %+v, %v", got, err)
	}
	if err := acme.CreateTrigger(ctx, store.Trigger{
		ID: "legacy", Pipeline: "build", GitSHA: sha, GithubOwner: "acme", GithubRepo: "widgets", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err = acme.GitHubCommitTriggers(ctx, store.GitHubRepo{ID: repoID, Owner: "acme", Name: "widgets"}, sha)
	if err != nil || len(got) != 2 {
		t.Fatalf("legacy trigger matched a repository id it never recorded: %+v, %v", got, err)
	}
	got, err = acme.GitHubCommitTriggers(ctx, store.GitHubRepo{Owner: "acme", Name: "widgets"}, sha)
	if err != nil || len(got) != 0 {
		t.Fatalf("lookup without a repository id matched runs: %+v, %v", got, err)
	}
}

func TestGitHubAppRepositoryKeysSurviveRenamesProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(193, 811))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			st := storetest.Open(t)
			targets := []*store.Tenant{teamHandle(t, st, "acme"), teamHandle(t, st, "other")}
			ids := []int64{1 + random.Int64N(1<<60), 1 + random.Int64N(1<<60)}
			now := time.Unix(1700000000, int64(1+random.IntN(999999999))).UTC()
			for j, target := range targets {
				installation := acmeInstallation()
				installation.InstallationID += int64(j)
				if _, err := target.BindGitHubAppInstallation(t.Context(), installation, now); err != nil {
					t.Fatal(err)
				}
				for _, repository := range []string{"acme/widgets", "renamed/widgets"} {
					tr := store.GitHubAppTrigger{RepositoryID: ids[j], Repository: repository, InstallationID: installation.InstallationID, Pipeline: "ci", Push: true}
					if _, err := target.PutGitHubAppTrigger(t.Context(), tr, now); err != nil {
						t.Fatal(err)
					}
					a := store.GitHubAppAutomation{RepositoryID: ids[j], Repository: repository, InstallationID: installation.InstallationID, EnabledBy: "account"}
					if err := target.PutGitHubAppAutomation(t.Context(), a, now); err != nil {
						t.Fatal(err)
					}
				}
			}
			for j, target := range targets {
				for _, table := range []string{"github_app_triggers", "github_app_automation"} {
					var key, display string
					query := storetest.Rebind(st, "SELECT repo_key,repository FROM "+table+" WHERE team=? AND repository_id=?")
					if err := st.DB().QueryRow(query, target.Team(), ids[j]).Scan(&key, &display); err != nil {
						t.Fatal(err)
					}
					if key != fmt.Sprintf("gh:%d", ids[j]) || display != "renamed/widgets" {
						t.Fatalf("table=%s team=%s key=%s repository=%s", table, target.Team(), key, display)
					}
				}
				rows, err := target.GitHubAppTriggers(t.Context())
				if err != nil || len(rows) != 1 || !rows[0].CreatedAt.Equal(now) {
					t.Fatalf("subscription timestamp lost nanoseconds: %+v, %v", rows, err)
				}
			}
		})
	}
}
