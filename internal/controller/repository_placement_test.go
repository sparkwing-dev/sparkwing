package controller_test

import (
	"context"
	"io"
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

func TestGitHubAppUnverifiedRunStillSealsItsPlacement(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(defaultPlacementConfig))
	if code := f.subscribe(olga, "acme/widgets", "deploy", nil); code != http.StatusOK {
		t.Fatalf("subscribe=%d", code)
	}
	cfg := f.app.Config()
	cfg.HTTP = &http.Client{Transport: automationTransport(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/compare/") {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	f.srv.WithGitHubApp(cfg)
	if code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), ""); code != http.StatusAccepted || out["status"] != "dispatched" {
		t.Fatalf("push=%d %v", code, out)
	}
	tenant, err := f.store.ForTeam(context.Background(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(context.Background(), f.triggers(olga.team)[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Request.Provenance.ProvenRepository() != "" || run.Request.Request.Placement == nil {
		t.Fatalf("an unverified App run dropped its list: proof=%+v placement=%+v", run.Request.Provenance, run.Request.Request.Placement)
	}
}

func TestGitHubAppUnreadablePlacementRefusesOnlyThatRun(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	if code := f.subscribe(olga, "acme/widgets", "deploy", map[string]any{"branches": []string{}}); code != http.StatusOK {
		t.Fatalf("subscribe=%d", code)
	}
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(defaultPlacementConfig))
	f.app.FailContents(1)
	code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), "")
	runs, _ := out["runs"].([]any)
	if code != http.StatusAccepted || len(runs) != 1 || runs[0].(map[string]any)["status"] != "refused" || !strings.Contains(runs[0].(map[string]any)["reason"].(string), "default-branch placement") {
		t.Fatalf("a placement read failure must refuse the run visibly, not fail the delivery: %d %v", code, out)
	}
	if len(f.triggers(olga.team)) != 0 {
		t.Fatal("a run started without its placement")
	}
}
