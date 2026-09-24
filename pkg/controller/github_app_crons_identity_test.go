package controller_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestGitHubAppCronRenameKeepsIdentityAndPauseAtRepoCap(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	f.deliver("push", cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main"), "")
	before := appCronRows(t, f, olga.team)[0]
	tn, err := f.store.ForTeam(t.Context(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.SetCronSchedulePaused(t.Context(), before.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := range 9 {
		if _, _, err := tn.ArmCronSchedule(t.Context(), store.CronSchedule{
			ID: fmt.Sprintf("crn_seed_%d", i), RepoPath: fmt.Sprintf("https://example.com/r%d.git", i),
			Pipeline: "seed", Name: "default", Cron: "0 1 * * *", TZ: "UTC",
			Overlap: store.CronOverlapSkip, CatchUp: time.Hour, Where: store.CronWhereController,
		}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	f.app.SetRepos(7,
		githubRepo(701, "acme/renamed"), githubRepo(702, "acme/plans"))
	next := strings.Repeat("b", 40)
	f.app.SetCommit("acme/renamed", "main", next)
	f.app.SetFile("acme/renamed", next, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	if code, _ := f.deliver("repository", map[string]any{
		"action": "renamed", "installation": map[string]any{"id": 7},
		"repository": map[string]any{"id": 701, "full_name": "acme/renamed"},
	}, ""); code != http.StatusOK {
		t.Fatalf("rename event = %d", code)
	}
	if code, _ := f.deliver("push", cronPush(7, 701, "acme/renamed", next, "refs/heads/main"), ""); code != http.StatusAccepted {
		t.Fatalf("renamed repo at cap = %d", code)
	}
	var matched []store.CronSchedule
	for _, row := range appCronRows(t, f, olga.team) {
		if row.GitHubRepositoryID == 701 {
			matched = append(matched, row)
		}
	}
	if len(matched) != 1 || matched[0].ID != before.ID || !matched[0].Paused ||
		matched[0].RepoPath != "https://github.com/acme/renamed.git" || matched[0].LockedRef != next {
		t.Fatalf("renamed App schedule = %+v, before %+v", matched, before)
	}
}

func TestGitHubAppCronLifecycleWithdrawsOldBindings(t *testing.T) {
	for _, action := range []string{"uninstall", "team_unbind", "operator_unbind", "suspend", "repository_removed", "selection_changed", "repository_removed_api_failure", "repository_deleted", "transfer_unconnected"} {
		t.Run(action, func(t *testing.T) {
			f := newAppFixture(t)
			olga := f.ghUser(501, "olga")
			f.connect(olga, 501, 7, acmeAdmin)
			f.app.SetCommit("acme/widgets", "main", headSHA)
			f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
			f.deliver("push", cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main"), "")
			switch action {
			case "uninstall":
				f.deliver("installation", map[string]any{"action": "deleted", "installation": map[string]any{"id": 7}}, "")
			case "team_unbind":
				if code := f.call("DELETE", "/api/v1/team/github-app/installations/7", olga.auth, nil, nil); code != http.StatusNoContent {
					t.Fatalf("team unbind = %d", code)
				}
			case "operator_unbind":
				if code := f.call("DELETE", "/api/v1/github-app/installations/7", "Bearer "+f.admin, nil, nil); code != http.StatusNoContent {
					t.Fatalf("operator unbind = %d", code)
				}
			case "suspend":
				f.deliver("installation", map[string]any{"action": "suspend", "installation": map[string]any{"id": 7}}, "")
			case "repository_removed", "selection_changed", "repository_removed_api_failure":
				f.app.SetRepos(7, githubRepo(702, "acme/plans"))
				removed := []any{map[string]any{"id": 701, "full_name": "acme/widgets"}}
				if action == "selection_changed" {
					removed = []any{}
				}
				if action == "repository_removed_api_failure" {
					f.app.FailInstallationRepositories(1)
				}
				code, _ := f.deliver("installation_repositories", map[string]any{
					"action": "removed", "installation": map[string]any{"id": 7},
					"repositories_removed": removed,
				}, "")
				if action == "repository_removed_api_failure" && code != http.StatusBadGateway {
					t.Fatalf("removal API failure = %d, want 502", code)
				}
			case "transfer_unconnected":
				f.app.SetRepos(7, githubRepo(702, "acme/plans"))
				f.deliver("repository", map[string]any{
					"action": "transferred", "installation": map[string]any{"id": 7},
					"repository": map[string]any{"id": 701, "full_name": "new-owner/widgets"},
				}, "")
			case "repository_deleted":
				f.deliver("repository", map[string]any{
					"action": "deleted", "installation": map[string]any{"id": 7},
					"repository": map[string]any{"id": 701, "full_name": "acme/widgets"},
				}, "")
			}
			rows := appCronRows(t, f, olga.team)
			if len(rows) != 1 || rows[0].Declared {
				t.Fatalf("%s left old schedule armed: %+v", action, rows)
			}
		})
	}
}

func TestGitHubAppCronTransferWithdrawsFormerTeamBeforeNewArm(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	f.deliver("push", cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main"), "")
	f.app.SetRepos(7, githubRepo(702, "acme/plans"))
	f.app.SetRepos(8, githubRepo(701, "bob/widgets"))
	f.connect(bob, 502, 8, nil)
	next := strings.Repeat("e", 40)
	f.app.SetCommit("bob/widgets", "main", next)
	f.app.SetFile("bob/widgets", next, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	if code, _ := f.deliver("push", cronPush(8, 701, "bob/widgets", next, "refs/heads/main"), ""); code != http.StatusAccepted {
		t.Fatalf("new team's push = %d", code)
	}
	oldRows, newRows := appCronRows(t, f, olga.team), appCronRows(t, f, bob.team)
	if len(oldRows) != 1 || oldRows[0].Declared || len(newRows) != 1 || !newRows[0].Declared ||
		oldRows[0].ID == newRows[0].ID || newRows[0].GitHubRepositoryID != 701 {
		t.Fatalf("transfer schedules old=%+v new=%+v", oldRows, newRows)
	}
}

func TestGitHubAppCronConflictsWithManualControllerSchedule(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	tn, err := f.store.ForTeam(t.Context(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tn.ArmCronSchedule(t.Context(), store.CronSchedule{
		ID: "crn_manual", RepoPath: "git@github.com:acme/widgets.git", Pipeline: "nightly", Name: "default",
		Cron: "0 3 * * *", TZ: "UTC", Overlap: store.CronOverlapSkip, CatchUp: time.Hour,
		Where: store.CronWhereController,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	if code, _ := f.deliver("push", cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main"), ""); code != http.StatusConflict {
		t.Fatalf("manual conflict = %d, want 409", code)
	}
	rows := appCronRows(t, f, olga.team)
	if len(rows) != 1 || rows[0].GitHubRepositoryID != 0 || !rows[0].Declared {
		t.Fatalf("manual schedule was adopted or duplicated: %+v", rows)
	}
}

func TestGitHubAppCronRenameConflictStopsOldAppRow(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(githubCronConfig))
	f.deliver("push", cronPush(7, 701, "acme/widgets", headSHA, "refs/heads/main"), "")
	tn, err := f.store.ForTeam(t.Context(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := tn.ArmCronSchedule(t.Context(), store.CronSchedule{
		ID: "crn_manual_renamed", RepoPath: "https://github.com/acme/renamed.git",
		Pipeline: "nightly", Name: "default", Cron: "0 4 * * *", TZ: "UTC",
		Overlap: store.CronOverlapSkip, CatchUp: time.Hour, Where: store.CronWhereController,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.app.SetRepos(7, githubRepo(701, "acme/renamed"), githubRepo(702, "acme/plans"))
	if code, _ := f.deliver("repository", map[string]any{
		"action": "renamed", "installation": map[string]any{"id": 7},
		"repository": map[string]any{"id": 701, "full_name": "acme/renamed"},
	}, ""); code != http.StatusConflict {
		t.Fatalf("conflicting rename = %d, want 409", code)
	}
	rows := appCronRows(t, f, olga.team)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	for _, row := range rows {
		if row.GitHubRepositoryID == 701 && row.Declared {
			t.Fatalf("conflicting App row still fires: %+v", row)
		}
	}
}

func githubRepo(id int64, fullName string) githubapptest.Repo {
	return githubapptest.Repo{ID: id, FullName: fullName}
}
