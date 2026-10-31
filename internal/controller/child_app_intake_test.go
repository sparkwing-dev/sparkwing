package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAppParentChildrenPreserveOnlyExactRevisionTrust(t *testing.T) {
	for _, kind := range []string{"exact", "changed", "cross-repository"} {
		t.Run(kind, func(t *testing.T) {
			repo, original := sourcetest.Repository(t)
			sourcetest.Git(t, repo, "checkout", "-qb", "deploy")
			sourcetest.Write(t, filepath.Join(repo, ".sparkwing", "pipeline.go"), "package main // child\n")
			sourcetest.Git(t, repo, "add", ".")
			sourcetest.Git(t, repo, "commit", "-qm", "child")
			selected := sourcetest.Git(t, repo, "rev-parse", "HEAD")
			sourcetest.Remote(t, map[string]string{"https://github.com/acme/app": repo, "https://github.com/acme/child": repo})
			e, h, claim := childIntakeFixtureWith(t, func(e *engineFixture) {
				engineSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job","children":[{"pipeline":"deploy"},{"pipeline":"deploy","repository":"gh:2"}]}`))
				registerChildRepository(t, e, 2)
			})
			app := githubapptest.New(t)
			app.AddInstallation(githubapptest.Installation{ID: 7, Account: githubapp.Account{ID: 70, Login: "acme", Type: "Organization"}, Repos: []githubapptest.Repo{{ID: 1, FullName: "acme/app", DefaultBranch: "trunk"}, {ID: 2, FullName: "acme/child", DefaultBranch: "trunk"}}})
			e.Server.WithGitHubApp(app.Config())
			teststore.SeedGitHubInstallation(t, e.Store, "a", store.GitHubAppInstallation{InstallationID: 7, AccountID: 70, AccountLogin: "acme", AccountType: "Organization", ConnectedBy: "member", GitHubUserID: 71}, time.Now())
			tenant := teststore.ForTeam(t, e.Store, "a")
			parent, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			request := &parent.Request.Request
			facts := runspec.AppFacts{RepositoryID: 1, Repository: "acme/app", InstallationID: 7, Ref: "refs/heads/trunk", SHA: original, Event: "push", DefaultBranch: "trunk", DefaultBranchPush: true, DeliveryKey: "delivery", VerifiedAt: time.Now()}
			request.Origin = runspec.OriginApp
			request.Git = runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "github.com/acme/app"}, Ref: facts.Ref, SHA: original, App: runspec.Optional[runspec.AppFacts]{Set: true, Value: facts}}
			request.Source = runspec.Source{Ref: original, CommittedCheckout: true, Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app", PipelineDir: ".sparkwing"}}
			request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerActions}}, Origin: runspec.SettingOrigin{Layer: runspec.LayerOperatorTeam, Key: "github_app.runner"}}
			proof, err := appProvenance(t, facts, request.PullRequest, request.Pipeline).WithSourceCredentialVerified()
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, sealWith(t, parent.Request, proof))
			host, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: childRecorderTransport{h}}, Bearer: func(context.Context) (string, error) { return claim, nil }}, "run", "job")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = host.Close() })
			principal, err := host.Principal(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			process, err := host.ProcessClient(t.Context(), principal, "job")
			if err != nil {
				t.Fatal(err)
			}
			remote, branch := "", ""
			if kind != "exact" {
				branch = "deploy"
			}
			if kind == "cross-repository" {
				remote = "https://github.com/acme/child"
			}
			id, err := process.StartChild(t.Context(), "deploy", nil, remote, branch)
			if err != nil {
				t.Fatalf("POST children: %v", err)
			}
			child, err := tenant.ReadRun(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if child.Request.Request.Origin != runspec.OriginChild || child.Request.Request.Options.Runner.Value.Kind != runspec.RunnerActions {
				t.Fatalf("child lost its origin or inherited owner: %+v", child.Request.Request)
			}
			got := child.Request.Provenance
			if kind == "exact" {
				if got != proof {
					t.Fatalf("exact child changed seal: %+v, want %+v", got, proof)
				}
			} else if got.Trust() != runspec.TrustAsserted || got.ProvenRepository() != "" || got.SourceCredentialVerified() || got.DefaultBranchWork() || !got.SharedSecretsOnly() || child.Request.Request.Git.SHA != selected {
				t.Fatalf("changed child retained proof or selected wrong commit: %+v", child.Request)
			}
		})
	}
}
