package controller_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const defaultPlacementConfig = `pipelines:
  - name: deploy
    entrypoint: Deploy
    placement:
      prefer:
        - target: team
          labels: [os=darwin]
          hold: 5s
`

const branchPlacementConfig = `pipelines:
  - name: deploy
    entrypoint: Deploy
    placement:
      prefer:
        - target: cloud
`

func sealedPlacement(t *testing.T, f *appFixture, team string) *runspec.RepositoryPlacement {
	t.Helper()
	triggers := f.triggers(team)
	if len(triggers) != 1 {
		t.Fatalf("runs=%d, want 1", len(triggers))
	}
	tenant, err := f.store.ForTeam(context.Background(), store.Team(team))
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(context.Background(), triggers[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return run.Request.Request.Placement
}

func TestGitHubAppRunSealsDefaultBranchPlacementNotTheEventsOwn(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "deploy", map[string]any{"branches": []string{}}); code != http.StatusOK {
		t.Fatalf("subscribe=%d", code)
	}
	feature := strings.Repeat("2", 40)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(defaultPlacementConfig))
	f.app.SetFile("acme/widgets", feature, ".sparkwing/sparkwing.yaml", []byte(branchPlacementConfig))
	push := pushPayload(7, 701, "acme/widgets", feature)
	push["ref"] = "refs/heads/feature/paid"
	if code, out := f.deliver("push", push, ""); code != http.StatusAccepted || out["status"] != "dispatched" {
		t.Fatalf("push=%d %v", code, out)
	}
	policy := sealedPlacement(t, f, olga.team)
	if policy == nil || policy.Revision != headSHA || len(policy.Prefer) != 1 || policy.Prefer[0].Target != runspec.PlacementTeam || policy.Prefer[0].Labels[0] != "os=darwin" {
		t.Fatalf("sealed placement=%+v, want the default branch's team list at %s", policy, headSHA)
	}
}

func TestGitHubAppRunnerPinConflictsWithPlacement(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "deploy", map[string]any{"branches": []string{}, "runner": "cloud"}); code != http.StatusOK {
		t.Fatalf("subscribe=%d", code)
	}
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(defaultPlacementConfig))
	code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), "")
	if code == http.StatusAccepted || len(f.triggers(olga.team)) != 0 {
		t.Fatalf("a runner pin silently won over placement: %d %v", code, out)
	}
}
