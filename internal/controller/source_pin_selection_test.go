package controller_test

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

const pinnedSourceConfig = "pipelines:\n  - name: build\n    entrypoint: build\n    source: origin/main\n  - name: nightly\n    entrypoint: nightly\n    source: origin/main\n"

func pinnedSourceRepository(t *testing.T, remote string) (execution, code string) {
	t.Helper()
	repo, _ := sourcetest.Repository(t)
	sourcetest.Git(t, repo, "checkout", "-qb", "main")
	sourcetest.Write(t, filepath.Join(repo, ".sparkwing", "pipeline.go"), "package main // release\n")
	sourcetest.Git(t, repo, "commit", "-qam", "origin/main")
	code = sourcetest.Git(t, repo, "rev-parse", "HEAD")
	sourcetest.Git(t, repo, "checkout", "-q", "trunk")
	sourcetest.Git(t, repo, "checkout", "-qb", "execution")
	sourcetest.Write(t, filepath.Join(repo, ".sparkwing", "sparkwing.yaml"), pinnedSourceConfig)
	sourcetest.Git(t, repo, "add", ".")
	sourcetest.Git(t, repo, "commit", "-qm", "main")
	execution = sourcetest.Git(t, repo, "rev-parse", "HEAD")
	sourcetest.Remote(t, map[string]string{remote: repo})
	return execution, code
}

func assertPinnedSelection(t *testing.T, request runspec.Request, execution, code string) {
	t.Helper()
	if request.Git.SHA != execution || request.Source.Ref != code || request.Source.RequestedRef != "origin/main" {
		t.Fatalf("sealed execution %s code %s intent %q, want execution %s code %s from source: origin/main", request.Git.SHA, request.Source.Ref, request.Source.RequestedRef, execution, code)
	}
}

func TestGitHubAppRunSealsConfiguredCodePin(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "build", nil); code != http.StatusOK {
		t.Fatalf("subscribe = %d", code)
	}
	execution, code := pinnedSourceRepository(t, "https://github.com/acme/widgets.git")
	f.app.SetCommit("acme/widgets", "main", code)
	f.app.SetFile("acme/widgets", execution, ".sparkwing/sparkwing.yaml", []byte(pinnedSourceConfig))
	if status, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", execution), ""); status != http.StatusAccepted || out["status"] != "dispatched" {
		t.Fatalf("push = %d %v\nserver log:\n%s", status, out, f.logs.String())
	}
	got := f.triggers(olga.team)
	team, err := f.store.ForTeam(t.Context(), store.Team(olga.team))
	if err != nil || len(got) != 1 {
		t.Fatalf("triggers = %+v, %v", got, err)
	}
	run, err := team.ReadRun(t.Context(), got[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertPinnedSelection(t, run.Request.Request, execution, code)
	if run.Request.Request.Git.App.Value.SHA != execution {
		t.Fatal("verified App execution commit changed")
	}
}

func TestRemoteScheduleSealsConfiguredCodePin(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.4s measured in the short class; the full suite runs it")
	}
	f := newCronsFixture(t)
	remote := "https://8.8.8.8/acme/pinned.git"
	execution, code := pinnedSourceRepository(t, remote)
	f.push(map[string]any{"repo_url": remote, "branch": "execution", "sha": execution, "schedules": []map[string]any{{"pipeline": "nightly", "cron": "0 3 * * *"}}})
	var launched controllerwire.ScheduleRun
	f.call(http.MethodPost, "/api/v1/schedules/acme%2Fpinned%2Fnightly/run", f.writer, nil, http.StatusOK, &launched)
	run, err := teststore.ForTeam(t, f.store, store.DefaultTeam).ReadRun(t.Context(), launched.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertPinnedSelection(t, run.Request.Request, execution, code)
}
