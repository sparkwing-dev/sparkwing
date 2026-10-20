package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestCycleDetect_RejectsSelfCycle(t *testing.T) {
	_, h, claim := childIntakeFixture(t)
	w := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "ci", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 409)
	if !strings.Contains(w.Body.String(), "cycle") {
		t.Fatalf("cycle refusal: %s", w.Body)
	}
}

func TestCycleDetect_AllowsIndirectNonCycle(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	if err := teststore.SeedRun(e.Store, t.Context(), "a", store.Run{ID: "root", Pipeline: "root", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	setChildAncestor(t, e, "run", "root")
	engineSQL(t, e.Store, `UPDATE teams SET next_run_number=3 WHERE name='a'`)
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "deploy", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 201)
}

func TestCycleDetect_RejectsDeepCycle(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	for _, r := range []store.Run{{ID: "root", Pipeline: "root", Status: "success"}, {ID: "mid", Pipeline: "mid", Status: "running", ParentRunID: "root"}} {
		if err := teststore.SeedRun(e.Store, t.Context(), "a", r); err != nil {
			t.Fatal(err)
		}
	}
	setChildAncestor(t, e, "mid", "root")
	setChildAncestor(t, e, "run", "mid")
	engineSQL(t, e.Store, `UPDATE teams SET next_run_number=4 WHERE name='a'`)
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "root", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 409)
}

func TestTrigger_ParentRepoInheritance(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	remote, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: childRecorderTransport{h}}, Bearer: func(context.Context) (string, error) { return claim, nil }}, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := remote.Close(); err != nil {
			t.Error(err)
		}
	})
	principal, err := remote.Principal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := remote.ProcessClient(t.Context(), principal, "job")
	if err != nil {
		t.Fatal(err)
	}
	id, err := process.StartChild(t.Context(), "deploy", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if run.Request.Request.Git != parent.Request.Request.Git || run.Request.Request.Source != parent.Request.Request.Source {
		t.Fatal("implicit child did not inherit sealed parent source")
	}
	git := run.Request.Request.Git
	if git.Repository.Key != "gh:1" || git.Ref != "main" || git.SHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("source: %+v", git)
	}
	if run.Request.Request.Origin != runspec.OriginChild || run.Lineage.ParentRunID != "run" || run.Lineage.ParentJobID != "job" {
		t.Fatalf("lineage: %+v", run.Lineage)
	}
}

func TestCycleDetect_ParentNotFound(t *testing.T) {
	_, h, claim := childIntakeFixture(t)
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/missing/children", childIntakeBody(t, "deploy", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 404)
}

func TestChildCycleWalkStaysInsideItsTeam(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	teststore.SeedTeam(t, e.Store, "foreign")
	if err := teststore.SeedRun(e.Store, t.Context(), "foreign", store.Run{ID: "private-root", Pipeline: "private-pipeline", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	setChildAncestor(t, e, "run", "private-root")
	w := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "deploy", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 404)
	if strings.Contains(w.Body.String(), "private-pipeline") || strings.Contains(w.Body.String(), "private-root") {
		t.Fatalf("foreign ancestry disclosed: %s", w.Body)
	}
}

type childRecorderTransport struct{ handler http.Handler }

func (c childRecorderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func setChildAncestor(t *testing.T, e *engineFixture, id, parent string) {
	t.Helper()
	var body []byte
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT lineage_json FROM runs WHERE team='a' AND id=?`, id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	lineage := map[string]any{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &lineage); err != nil {
			t.Fatal(err)
		}
	}
	lineage["parent_run_id"] = parent
	body, err := json.Marshal(lineage)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET lineage_json=? WHERE team='a' AND id=?`, body, id)
}

// The same pipeline name in another authorized repository is not a cycle and
// cannot inherit the first repository's identity, even at the same ref and SHA.
func TestAuthorizedCrossRepositoryChildIsAsserted(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"registration", "app_coverage"} {
		t.Run(source, func(t *testing.T) {
			e, h, claim := childIntakeFixtureWith(t, func(e *engineFixture) {
				if source == "registration" {
					registerChildRepository(t, e, 2)
				} else {
					teststore.SeedGitHubInstallation(t, e.Store, "a", store.GitHubAppInstallation{InstallationID: 7, AccountID: 70, AccountLogin: "acme", AccountType: "Organization", ConnectedBy: "owner", GitHubUserID: 71}, time.Now())
					cfg := githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
						var payload string
						switch r.URL.Path {
						case "/app/installations/7/access_tokens":
							payload = `{"token":"private","expires_at":"2100-01-01T00:00:00Z","permissions":{"metadata":"read"}}`
						case "/installation/repositories":
							payload = `{"total_count":1,"repositories":[{"id":2,"full_name":"acme/child","owner":{"id":70,"login":"acme"}}]}`
						default:
							return nil, fmt.Errorf("unexpected GitHub request %s", r.URL)
						}
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
					})}}
					New(e.Store, nil).WithGitHubApp(cfg)
				}
				engineSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job","children":[{"pipeline":"ci","repository":"gh:2"},{"pipeline":"ci","repository":"gh:3"}]}`))
			})
			issued, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(claim)))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(issued.Capability.Repositories, []string{"gh:1"}) || issued.Capability.Data.RepositoryKey != "gh:1" || !issued.Capability.AllowsChild("ci", "gh:2") || issued.Capability.AllowsChild("ci", "gh:3") {
				t.Fatalf("child authority widened source/data scope or ignored team consent: %+v", issued.Capability)
			}
			engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "ci", runspec.Git{Repository: runspec.Repository{Key: "gh:3"}}), 403)
			engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "deploy", runspec.Git{Repository: runspec.Repository{Key: "gh:2"}}), 403)
			w := engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "ci", runspec.Git{Repository: runspec.Repository{Key: "gh:2"}}), 201)
			var result struct {
				Request runspec.SealedRequest `json:"request"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			p := result.Request.Provenance
			trigger, repo, ref, sha := p.OIDC()
			if p.Trust() != runspec.TrustAsserted || !p.SharedSecretsOnly() || trigger != "manual" || repo != 0 || ref != "" || sha != "" {
				t.Fatalf("explicit child inherited identity: %+v", p)
			}
		})
	}
}

func TestChildCycleWalkRefusesRepeatedAncestorIDs(t *testing.T) {
	e, h, claim := childIntakeFixture(t)
	if err := teststore.SeedRun(e.Store, t.Context(), "a", store.Run{ID: "root", Pipeline: "root", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	setChildAncestor(t, e, "root", "run")
	setChildAncestor(t, e, "run", "root")
	engineClaimHTTP(t, h, claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "fresh", runspec.Git{Repository: runspec.Repository{Key: "gh:1"}}), 409)
	var count int
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("cyclic lineage persisted a child: %d: %v", count, err)
	}
}

func registerChildRepository(t *testing.T, e *engineFixture, id int64) {
	t.Helper()
	_, owner, err := teststore.ProvisionCredential(t.Context(), e.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "child-owner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.TeamAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Principal: capability.Principal{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: owner.Prefix}}, Target: capability.Target{Team: "a"}, Access: capability.AccessRule{Credential: capability.CredentialMember, Grants: []capability.Grant{capability.TeamAdmin}, Binding: capability.BindNone, Liveness: capability.LiveCredential}}
	if _, err := tenant.RegisterGitHubRunner(t.Context(), a, store.GitHubRunnerBinding{RepositoryID: id, RepositoryOwnerID: 7, Repository: "acme/child"}); err != nil {
		t.Fatal(err)
	}
}
