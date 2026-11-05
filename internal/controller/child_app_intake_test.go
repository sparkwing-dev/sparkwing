package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAppParentChildrenPreserveOnlyExactRevisionTrust(t *testing.T) {
	const pinnedConfig = "pipelines:\n  - name: deploy\n    entrypoint: deploy\n    source: refs/heads/code\n"
	for _, kind := range []string{"exact", "changed", "fetch-identical", "fetch-ahead", "fetch-unreachable", "fetch-behind", "fetch-unknown", "fetch-403", "fetch-422", "fetch-429", "fetch-503", "fetch-network", "fetch-metadata-403", "fetch-retry", "fetch-pinned", "fetch-pinned-source-unreachable", "fetch-pinned-execution-unreachable", "cross-repository"} {
		t.Run(kind, func(t *testing.T) {
			pinned := strings.HasPrefix(kind, "fetch-pinned")
			repo, original := sourcetest.Repository(t)
			if pinned {
				sourcetest.Git(t, repo, "branch", "code", original)
				sourcetest.Write(t, filepath.Join(repo, ".sparkwing", "sparkwing.yaml"), pinnedConfig)
			}
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
			app.SetCommit("acme/app", "refs/heads/deploy", selected)
			comparison := "identical"
			switch kind {
			case "fetch-ahead":
				comparison = "ahead"
			case "fetch-unreachable", "fetch-pinned-execution-unreachable":
				comparison = "diverged"
			case "fetch-behind":
				comparison = "behind"
			case "fetch-unknown":
				comparison = "unknown"
			}
			app.SetComparison("acme/app", selected, "refs/heads/deploy", comparison)
			if pinned {
				app.SetCommit("acme/app", "refs/heads/code", original)
				app.SetFile("acme/app", selected, ".sparkwing/sparkwing.yaml", []byte(pinnedConfig))
				sourceComparison := "ahead"
				if kind == "fetch-pinned-source-unreachable" {
					sourceComparison = "diverged"
				}
				app.SetComparison("acme/app", original, "refs/heads/code", sourceComparison)
			}
			compares := 0
			config := app.Config()
			config.HTTP = &http.Client{Transport: intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
				status := 0
				if strings.Contains(r.URL.Path, "/compare/") {
					compares++
					switch kind {
					case "fetch-403":
						status = http.StatusForbidden
					case "fetch-422":
						status = http.StatusUnprocessableEntity
					case "fetch-429":
						status = http.StatusTooManyRequests
					case "fetch-503":
						status = http.StatusServiceUnavailable
					case "fetch-network":
						return nil, errors.New("connection lost")
					}
				}
				if kind == "fetch-metadata-403" && compares > 0 && r.URL.Path == "/installation/repositories" {
					status = http.StatusForbidden
				}
				if status != 0 {
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"unavailable"}`)), Request: r}, nil
				}
				return http.DefaultTransport.RoundTrip(r)
			})}
			e.Server.WithGitHubApp(config)
			e.Server.sourceLookup = fixtureCloneHostLookup
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
			runner := runspec.RunnerActions
			if strings.HasPrefix(kind, "fetch-") || kind == "cross-repository" {
				runner = runspec.RunnerAny
			}
			request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runner}}, Origin: runspec.SettingOrigin{Layer: runspec.LayerOperatorTeam, Key: "github_app.runner"}}
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
			if child.Request.Request.Origin != runspec.OriginChild || child.Request.Request.Options.Runner.Value.Kind != runner {
				t.Fatalf("child lost its origin or inherited owner: %+v", child.Request.Request)
			}
			if pinned && (child.Request.Request.Source.Ref != original || child.Request.Request.Git.SHA != selected) {
				t.Fatalf("child lost separate source and execution revisions: %+v", child.Request.Request)
			}
			got := child.Request.Provenance
			if kind == "exact" {
				if got != proof {
					t.Fatalf("exact child changed seal: %+v, want %+v", got, proof)
				}
			} else if got.Trust() != runspec.TrustAsserted || got.ProvenRepository() != "" || got.SourceCredentialVerified() || got.DefaultBranchWork() || !got.SharedSecretsOnly() || got.Writer() != runspec.RunWriter("run") || got.DataWriter() != runspec.RunWriter("run") || got.UnverifiedReason() != "" || child.Request.Request.Git.SHA != selected {
				t.Fatalf("changed child retained proof or selected wrong commit: %+v", child.Request)
			}
			wantSource := runspec.RepoKey("")
			if kind == "changed" || kind == "fetch-identical" || kind == "fetch-ahead" || kind == "fetch-pinned" || kind == "fetch-retry" {
				wantSource = "gh:1"
			}
			if got.ChildSourceRepository() != wantSource {
				t.Fatalf("child source repository=%q, want %q", got.ChildSourceRepository(), wantSource)
			}
			if runner != runspec.RunnerAny {
				return
			}
			if kind == "fetch-retry" {
				before := compares
				engineSQL(t, e.Store, `UPDATE runs SET status='failed',finished_at=2 WHERE team='a' AND id=?`, id)
				bearer, _, err := teststore.ProvisionCredential(t.Context(), e.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "retrier"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsControl}})
				if err != nil {
					t.Fatal(err)
				}
				w := engineClaimHTTP(t, h, bearer, "POST", "/api/v1/runs/"+id+"/retry", `{"mode":"all","idempotency_key":"child-retry"}`, http.StatusOK)
				var retried controllerwire.CreateRunResponse
				if err := json.Unmarshal(w.Body.Bytes(), &retried); err != nil {
					t.Fatal(err)
				}
				child, err = tenant.ReadRun(t.Context(), retried.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if child.Request.Provenance != got || child.Lineage.RetryOf != id || compares != before || before == 0 {
					t.Fatalf("retry lost child seal or repeated compare: proof=%+v lineage=%+v compares=%d, before=%d", child.Request.Provenance, child.Lineage, compares, before)
				}
				id = retried.RunID
			}
			engineSQL(t, e.Store, `INSERT INTO github_app_triggers(team,repository_id,pipeline,repository,installation_id,created_at,repo_key) VALUES ('a',1,'ci','acme/app',7,1,'gh:1')`)
			if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "child-test", CreatedBy: "fixture"}); err != nil {
				t.Fatal(err)
			}
			launcher := engineHTTPRoot(t, e, "", "child-launcher", []capability.Grant{capability.ClaimsLaunch})
			w := engineClaimHTTP(t, h, launcher, "POST", "/api/v1/launcher/claim", fmt.Sprintf(`{"runner_version":"v1","kinds":["plan"],"run_id":%q}`, id), http.StatusOK)
			var award controllerwire.ClaimResponse
			if err := json.Unmarshal(w.Body.Bytes(), &award); err != nil || award.Claim == nil {
				t.Fatalf("child claim=%s err=%v", w.Body, err)
			}
			w = engineClaimHTTP(t, h, award.Claim.Capability, "POST", "/api/v1/claims/exchange", `{}`, http.StatusOK)
			var exchanged controllerwire.ExchangeResponse
			if err := json.Unmarshal(w.Body.Bytes(), &exchanged); err != nil {
				t.Fatal(err)
			}
			token := exchanged.Claim.Capability
			before := len(app.Minted())
			status := http.StatusOK
			if wantSource == "" {
				status = http.StatusNotFound
			}
			w = engineClaimHTTP(t, h, token, "POST", "/api/v1/runs/"+id+"/source-credential", "", status)
			if wantSource == "" {
				if len(app.Minted()) != before || !strings.Contains(w.Body.String(), "reachable") {
					t.Fatalf("unverified child source response=%s", w.Body)
				}
				return
			}
			var credential SourceCredentialResponse
			if err := json.Unmarshal(w.Body.Bytes(), &credential); err != nil {
				t.Fatal(err)
			}
			minted := app.Minted()[before:]
			if len(minted) != 1 || !slices.Equal(minted[0].RepositoryIDs, []int64{1}) || len(minted[0].Permissions) != 1 || minted[0].Permissions["contents"] != "read" || !app.TokenCovers(credential.Secret, "acme/app") || app.TokenCovers(credential.Secret, "acme/child") || len(credential.ExtraRepositories) != 0 {
				t.Fatalf("child source token widened scope: %+v", minted)
			}
			spec, err := source.ForRequest(child.Request)
			if err != nil {
				t.Fatal(err)
			}
			if pinned && spec.PipelineSHA != original {
				t.Fatalf("child checkout lost pinned pipeline commit: %+v", spec)
			}
			burstAcceptanceCheckout(t, client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: childRecorderTransport{h}}, Bearer: func(context.Context) (string, error) { return token, nil }}, id, spec)
		})
	}
}
