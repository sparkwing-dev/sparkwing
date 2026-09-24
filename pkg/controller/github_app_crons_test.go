package controller_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func appCronRows(t *testing.T, f *appFixture, team string) []store.CronSchedule {
	t.Helper()
	tn, err := f.store.ForTeam(t.Context(), store.Team(team))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tn.ListCronSchedules(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func cronPush(installation, repoID int64, repo, sha, ref string) map[string]any {
	p := pushPayload(installation, repoID, repo, sha)
	p["ref"] = ref
	p["repository"].(map[string]any)["default_branch"] = "main"
	return p
}

const githubCronConfig = `pipelines:
  - name: nightly
    entrypoint: Nightly
    on:
      schedule:
        cron: "0 3 * * *"
        where: controller
  - name: local-only
    entrypoint: LocalOnly
    on:
      schedule:
        cron: "0 4 * * *"
        where: local
`

func TestGitHubAppDefaultPushArmsAndWithdrawsControllerSchedules(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	payload := cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main")
	if code, _ := f.deliver("push", payload, ""); code != http.StatusAccepted {
		t.Fatalf("default push = %d", code)
	}
	rows := appCronRows(t, f, olga.team)
	if len(rows) != 1 || rows[0].Pipeline != "nightly" || rows[0].LockedRef != headSHA || !rows[0].Declared {
		t.Fatalf("armed schedules = %+v", rows)
	}
	minted := f.app.Minted()
	if len(minted) == 0 {
		t.Fatal("schedule config read minted no installation token")
	}
	if last := minted[len(minted)-1]; len(last.Repositories) != 1 || last.Repositories[0] != "widgets" ||
		len(last.Permissions) != 1 || last.Permissions["contents"] != "read" {
		t.Fatalf("schedule config token = %+v, want repository-scoped contents:read", last)
	}
	if rows := appCronRows(t, f, bob.team); len(rows) != 0 {
		t.Fatalf("another team sees schedules = %+v", rows)
	}
	if code, _ := f.deliver("push", payload, ""); code != http.StatusAccepted || len(appCronRows(t, f, olga.team)) != 1 {
		t.Fatalf("replayed push = %d, rows %+v", code, appCronRows(t, f, olga.team))
	}

	next := strings.Repeat("b", 40)
	f.app.SetCommit("acme/widgets", "main", next)
	if code, _ := f.deliver("push", cronPush(7, 701, "acme/widgets", next, "refs/heads/main"), ""); code != http.StatusAccepted {
		t.Fatalf("push without config = %d", code)
	}
	rows = appCronRows(t, f, olga.team)
	if len(rows) != 1 || rows[0].Declared {
		t.Fatalf("removed declaration = %+v, want withdrawn row", rows)
	}
}

func TestGitHubAppCronPushGuardsPreserveExistingSchedules(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	f.deliver("push", cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main"), "")

	for _, p := range []map[string]any{
		cronPush(7, 701, "acme/widgets", strings.Repeat("1", 40), "refs/heads/feature"),
		cronPush(7, 701, "acme/widgets", strings.Repeat("2", 40), "refs/tags/v1"),
		cronPush(7, 701, "acme/widgets", strings.Repeat("3", 40), "refs/heads/main"),
		cronPush(7, 999, "acme/widgets", headSHA, "refs/heads/main"),
	} {
		f.deliver("push", p, "")
		rows := appCronRows(t, f, olga.team)
		if len(rows) != 1 || rows[0].LockedRef != headSHA || !rows[0].Declared {
			t.Fatalf("ignored push changed schedule: %+v", rows)
		}
	}
	next := strings.Repeat("b", 40)
	f.app.SetCommit("acme/widgets", "main", next)
	f.app.SetFile("acme/widgets", next, ".sparkwing/sparkwing.yaml", []byte("pipelines: [invalid"))
	f.app.FailContents(1)
	if code, _ := f.deliver("push", cronPush(7, 701, "acme/widgets", next, "refs/heads/main"), ""); code != http.StatusBadGateway {
		t.Fatalf("GitHub API failure = %d, want 502", code)
	}
	if rows := appCronRows(t, f, olga.team); len(rows) != 1 || rows[0].LockedRef != headSHA {
		t.Fatalf("GitHub API failure changed schedule: %+v", rows)
	}
	if code, _ := f.deliver("push", cronPush(7, 701, "acme/widgets", next, "refs/heads/main"), ""); code < 400 {
		t.Fatalf("bad config = %d, want failure", code)
	}
	rows := appCronRows(t, f, olga.team)
	if len(rows) != 1 || rows[0].LockedRef != headSHA || !rows[0].Declared {
		t.Fatalf("failed read or parse changed schedule: %+v", rows)
	}
	if code, _ := f.deliver("installation", map[string]any{"action": "deleted", "installation": map[string]any{"id": 7}}, ""); code != http.StatusOK {
		t.Fatalf("uninstall = %d", code)
	}
	f.deliver("push", cronPush(7, 701, "acme/widgets", next, "refs/heads/main"), "")
	if rows := appCronRows(t, f, olga.team); len(rows) != 1 || rows[0].LockedRef != headSHA {
		t.Fatalf("uninstalled push changed schedule: %+v", rows)
	}
}

func TestGitHubAppCronFailureDoesNotSuppressSubscribedPush(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	f.deliver("push", cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main"), "")
	if code := f.subscribe(olga, "acme/widgets", "build", nil); code != http.StatusOK {
		t.Fatalf("subscribe = %d", code)
	}
	for i, tc := range []struct {
		sha     string
		content []byte
		failAPI bool
	}{
		{sha: strings.Repeat("b", 40), content: []byte("pipelines: [invalid")},
		{sha: strings.Repeat("c", 40), content: []byte(githubCronConfig), failAPI: true},
	} {
		f.app.SetCommit("acme/widgets", "main", tc.sha)
		f.app.SetFile("acme/widgets", tc.sha, ".sparkwing/sparkwing.yaml", tc.content)
		if tc.failAPI {
			f.app.FailContents(1)
		}
		if code, _ := f.deliver("push", cronPush(7, 701, "acme/widgets", tc.sha, "refs/heads/main"), ""); code != http.StatusAccepted {
			t.Fatalf("subscribed push with cron failure = %d, want 202", code)
		}
		if got := len(f.triggers(olga.team)); got != i+1 {
			t.Fatalf("push %d started %d subscribed runs, want %d", i, got, i+1)
		}
		rows := appCronRows(t, f, olga.team)
		if len(rows) != 1 || rows[0].LockedRef != headSHA || !rows[0].Declared {
			t.Fatalf("cron failure changed schedule: %+v", rows)
		}
	}
}

func TestGitHubAppStaleCronPushStillStartsSubscribedPipeline(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	f.deliver("push", cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main"), "")
	if code := f.subscribe(olga, "acme/widgets", "build", nil); code != http.StatusOK {
		t.Fatalf("subscribe = %d", code)
	}
	stale := strings.Repeat("d", 40)
	if code, _ := f.deliver("push", cronPush(7, 701, "acme/widgets", stale, "refs/heads/main"), ""); code != http.StatusAccepted {
		t.Fatalf("subscribed stale push = %d, want dispatch", code)
	}
	if n := len(f.triggers(olga.team)); n != 1 {
		t.Fatalf("stale push started %d subscribed runs, want one", n)
	}
	rows := appCronRows(t, f, olga.team)
	if len(rows) != 1 || rows[0].LockedRef != headSHA {
		t.Fatalf("stale push changed schedule: %+v", rows)
	}
}
