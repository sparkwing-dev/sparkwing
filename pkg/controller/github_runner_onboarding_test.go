package controller_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
)

func TestGitHubRunnerOnboardingResolvesCoveredIdentity(t *testing.T) {
	f := newAppFixture(t)
	owner := f.ghUser(501, "olga")
	f.connect(owner, 501, 7, acmeAdmin)
	var out struct {
		Bindings []struct {
			Repository string `json:"repository"`
			ID         int64  `json:"repository_id"`
			OwnerID    int64  `json:"repository_owner_id"`
		} `json:"bindings"`
		Workflow string `json:"workflow"`
	}
	if code := f.call("POST", "/api/v1/team/github-runners", owner.auth, map[string]string{"repository": "ACME/Widgets"}, &out); code != http.StatusCreated {
		t.Fatalf("bind = %d", code)
	}
	if len(out.Bindings) != 1 || out.Bindings[0].Repository != "acme/widgets" || out.Bindings[0].ID != 701 || out.Bindings[0].OwnerID != 70 || !strings.Contains(out.Workflow, owner.team) {
		t.Fatalf("binding response = %+v", out)
	}
}

func TestGitHubRunnerOnboardingRefusesUnverifiedIdentity(t *testing.T) {
	for _, scenario := range []string{"unbound", "uncovered", "forged ids", "transferred", "missing owner", "truncated", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAppFixture(t)
			owner := f.ghUser(501, "olga")
			if scenario != "unbound" {
				f.connect(owner, 501, 7, acmeAdmin)
			}
			req := map[string]any{"repository": "acme/widgets"}
			switch scenario {
			case "uncovered":
				req["repository"] = "bob/tools"
			case "forged ids":
				req["repository_id"] = 999
				req["repository_owner_id"] = 70
			case "transferred":
				f.app.SetRepos(7, githubapptest.Repo{ID: 701, FullName: "acme/widgets", Owner: githubapp.Account{ID: 999, Login: "acme"}})
			case "missing owner":
				f.app.SetRepos(7, githubapptest.Repo{ID: 701, FullName: "acme/widgets", Owner: githubapp.Account{Login: "acme"}})
			case "truncated":
				repos := []githubapptest.Repo{{ID: 701, FullName: "acme/widgets"}}
				for i := 0; i < 1000; i++ {
					repos = append(repos, githubapptest.Repo{ID: int64(1000 + i), FullName: fmt.Sprintf("acme/repo%d", i)})
				}
				f.app.SetRepos(7, repos...)
			case "unavailable":
				f.app.FailInstallationRepositories(1)
			}
			code := f.call("POST", "/api/v1/team/github-runners", owner.auth, req, nil)
			if code < 400 {
				t.Fatalf("bind = %d", code)
			}
			var listed struct {
				Bindings []any `json:"bindings"`
			}
			if code := f.call("GET", "/api/v1/team/github-runners", owner.auth, nil, &listed); code != http.StatusOK || len(listed.Bindings) != 0 {
				t.Fatalf("refused binding persisted: %d %+v", code, listed)
			}
		})
	}
}
