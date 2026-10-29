package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type intakeGitHubTransport func(*http.Request) (*http.Response, error)

func (f intakeGitHubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIntakeSourceNumericIdentityProperty(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	property := func(input string, forged bool) bool {
		s, session := credentialSessionServer(t)
		teststore.SeedGitHubInstallation(t, s.store, "a", store.GitHubAppInstallation{InstallationID: 7, AccountID: 70, AccountLogin: "acme", AccountType: "Organization", ConnectedBy: "member", GitHubUserID: 71}, time.Now())
		cfg := githubapp.Config{AppID: 1, Slug: "test", ClientID: "id", ClientSecret: "secret", WebhookSecret: "webhook", PrivateKey: key, HTTP: &http.Client{Transport: intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
			var payload string
			switch r.URL.Path {
			case "/repos/acme/app/installation":
				payload = `{"id":7,"app_id":1,"account":{"id":70,"login":"acme","type":"Organization"}}`
			case "/app/installations/7/access_tokens":
				payload = `{"token":"private","expires_at":"2100-01-01T00:00:00Z","permissions":{"metadata":"read"}}`
			case "/installation/repositories":
				payload = `{"total_count":1,"repositories":[{"id":42,"full_name":"acme/app","owner":{"id":70,"login":"acme"}}]}`
			default:
				return nil, fmt.Errorf("unexpected GitHub request %s", r.URL)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
		})}}
		s.WithGitHubApp(cfg)
		tenant, err := s.store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		request := runspec.Request{Version: 1, Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Display: input}, Ref: "main", SHA: strings.Repeat("a", 40)}, Source: runspec.Source{Ref: strings.Repeat("a", 40), Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app.git", PipelineDir: ".sparkwing"}}}
		if forged {
			request.Git.Repository.Key = "gh:43"
		}
		body, err := json.Marshal(map[string]any{"idempotency_key": "request", "request": request})
		if err != nil {
			t.Fatal(err)
		}
		w := resumeHTTP(s, session, "/api/v1/runs", string(body))
		triggers, err := tenant.ListTriggers(t.Context(), store.TriggerFilter{Pipelines: []string{"p"}})
		if err != nil {
			t.Fatal(err)
		}
		if forged {
			if w.Code != 403 || len(triggers) != 0 {
				t.Logf("forged HTTP %d: %s triggers=%d", w.Code, w.Body, len(triggers))
			}
			return w.Code == 403 && len(triggers) == 0
		}
		if w.Code != 201 || len(triggers) != 1 {
			t.Logf("HTTP %d: %s triggers=%d", w.Code, w.Body, len(triggers))
			return false
		}
		run, err := tenant.ReadRun(t.Context(), triggers[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		got := run.Request.Request
		if got.Team != "a" || got.Git.Repository.Key != "gh:42" || got.Source.Location.RemoteURL != request.Source.Location.RemoteURL || got.Git.App.Set {
			t.Logf("stored request=%+v", got)
			return false
		}
		return run.Request.AuthorityRef.Kind == "session" && triggers[0].IdempotencyKey == "request"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestIntakeSourceRefusesUnverifiedAndCredentialBearingLocations(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = intakeGitHubTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: http.NoBody}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	e, _ := engineRuntimeStore(t)
	s := e.Server
	for _, remote := range []string{"https://github.com/acme/app", "https://user:secret@example.com/repo", "https://example.com/repo?token=secret", "https://example.com/repo#secret", "file:///tmp/repo"} {
		request := runspec.Request{Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: remote}}}
		if err := s.resolveIntakeSource(t.Context(), "a", &request); err == nil {
			t.Fatalf("accepted source %q", remote)
		}
	}
	request := runspec.Request{Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: "https://gitlab.com/ACME/App.git"}}, Git: runspec.Git{Repository: runspec.Repository{Key: "git:gitlab.com/acme/app"}}}
	if err := s.resolveIntakeSource(t.Context(), "a", &request); err != nil || request.Git.Repository.Key != "git:gitlab.com/acme/app" {
		t.Fatalf("non-GitHub source: %+v, %v", request, err)
	}
}

func TestIntakeSourcePublicGitHubUsesProductionLookup(t *testing.T) {
	original := http.DefaultTransport
	calls := 0
	http.DefaultTransport = intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.github.com/repos/acme/app" || r.Header.Get("Authorization") != "" {
			t.Fatalf("public identity request: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":42}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	_, options, input, _ := sourceUploadFixture(t)
	input.Git.Repository = runspec.Repository{Key: "gh:42", Display: "acme/app"}
	input.Source.Location.RemoteURL = "https://github.com/acme/app.git"
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{IdempotencyKey: "github", Request: input})
	if err != nil || created.Inputs.Git.Repository.Key != "gh:42" || created.Inputs.Git.App.Set || calls < 1 {
		t.Fatalf("public GitHub intake: %+v calls=%d %v", created, calls, err)
	}
}

func TestIntakeSourcePrivateGitHubUsesStoredTeamCredential(t *testing.T) {
	s, options, input, _ := sourceUploadFixture(t)
	cipher, err := secrets.NewCipher([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	s.WithSecretsCipher(cipher)
	sealed, err := sealSecret(cipher, gitCredentialBinding(store.GitCredential{Team: "a", Host: "github.com", Repository: "gh:42"}), "team-read-token")
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `INSERT INTO git_credentials(host_wide,id,team,repository_key,host,kind,username,secret,confirmed_at,created_at,updated_at) VALUES (0,'source','a','gh:42','github.com','https','oauth',?,1,1,1)`, sealed)
	original := http.DefaultTransport
	githubID := int64(42)
	http.DefaultTransport = intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.github.com/repos/acme/app" || r.Header.Get("Authorization") != "Bearer team-read-token" {
			t.Fatal("private identity escaped the repository credential")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"id":%d}`, githubID)))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	input.Git.Repository = runspec.Repository{Key: "gh:42", Display: "acme/app"}
	input.Source.Location.RemoteURL = "https://github.com/acme/app.git"
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{IdempotencyKey: "private-github", Request: input})
	if err != nil || created.Inputs.Git.Repository.Key != "gh:42" || created.Inputs.Git.App.Set {
		t.Fatalf("private GitHub intake: %+v %v", created, err)
	}
	raw, err := json.Marshal(created)
	if err != nil || strings.Contains(string(raw), "team-read-token") {
		t.Fatal("source credential escaped into the receipt")
	}
	githubID = 43
	if _, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{IdempotencyKey: "mismatched-github", Request: input}); err == nil {
		t.Fatal("accepted a supplied repository key that GitHub did not verify")
	}
}

func TestIntakeSourceProofCannotBeChangedProperty(t *testing.T) {
	s, _, input, raw := sourceUploadFixture(t)
	p, err := s.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	input.Git.Repository = runspec.Repository{Key: "gh:42", Display: "acme/app"}
	input.Source.Location.RemoteURL = "https://github.com/acme/app.git"
	proof := verifiedIntakeSource{location: input.Source.Location, repository: input.Git.Repository}
	refused := func(ctx context.Context, request runspec.Request) bool {
		_, err := tenant.AdmitRun(ctx, store.IntakeRequest{Authority: p.Authority, Request: request})
		var failure *apierr.Error
		return errors.As(err, &failure) && failure.Code == apierr.Forbidden
	}
	if !refused(t.Context(), input) {
		t.Fatal("GitHub intake accepted an unverified source")
	}
	if err := quick.Check(func(seed []byte, which bool) bool {
		forged := input
		sum := fmt.Sprintf("%x", sha256.Sum256(seed))
		if which {
			forged.Git.Repository.Key = runspec.RepoKey("gh:" + sum)
		} else {
			forged.Source.Location.RemoteURL = "https://github.com/foreign/" + sum
		}
		return refused(context.WithValue(t.Context(), verifiedIntakeSourceKey{}, proof), forged)
	}, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceUploadNameScopeNeverTrustsCallerDisplayProperty(t *testing.T) {
	s, _, input, raw := sourceUploadFixture(t)
	parent, err := s.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := s.store.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: parent.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	http.DefaultTransport = intakeGitHubTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":42}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	h := s.Handler()
	if err := quick.Check(func(seed []byte) bool {
		forged := input
		sum := fmt.Sprintf("%x", sha256.Sum256(seed))
		forged.Git.Repository = runspec.Repository{Key: "gh:42", Display: "github.com/acme/" + sum}
		forged.Source.Location.RemoteURL = "https://github.com/foreign/" + sum
		body, err := json.Marshal(controllerwire.SourceUploadRequest{Git: forged.Git, Source: forged.Source, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 1})
		if err != nil {
			return false
		}
		return sourceUploadHTTP(h, restricted.Raw, "POST", "/api/v1/data/source", string(body)).Code == 403
	}, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestTrigger_RequiresAGitHubRepositorySlug(t *testing.T) {
	s, _, input, raw := sourceUploadFixture(t)
	original := http.DefaultTransport
	calls := 0
	http.DefaultTransport = intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.github.com/repos/sparkwing-dev/sparkwing" {
			t.Fatalf("unexpected identity lookup: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":42}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	h := s.Handler()
	for i, tc := range []struct {
		name, remote string
		want         int
	}{
		{"https", "https://github.com/sparkwing-dev/sparkwing.git", 201},
		{"ssh", "git@github.com:sparkwing-dev/sparkwing.git", 201},
		{"absent", "https://github.com", 400},
		{"https URL as slug", "https://github.com/https://attacker.example/payload.git", 400},
		{"scp URL as slug", "https://github.com/git@attacker.example:payload.git", 400},
		{"three segments", "https://github.com/owner/name/extra", 400},
		{"no slash", "https://github.com/sparkwing", 400},
		{"space", "https://github.com/owner/na me", 400},
		{"non-ascii", "https://github.com/ownér/name", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := input
			request.Git.Repository = runspec.Repository{}
			request.Source.Location.RemoteURL = tc.remote
			body, err := json.Marshal(controllerwire.CreateRunRequest{IdempotencyKey: fmt.Sprintf("slug-%d", i), Request: request})
			if err != nil {
				t.Fatal(err)
			}
			before := calls
			w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs", string(body), tc.want)
			if tc.want == 201 {
				var created controllerwire.CreateRunResponse
				if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
					t.Fatal(err)
				}
				if created.Inputs.Git.Repository.Key != "gh:42" || created.Inputs.Git.App.Set || calls <= before {
					t.Fatalf("unverified repository: %+v, calls=%d", created.Inputs.Git, calls-before)
				}
			} else if calls != before {
				t.Fatal("malformed source reached GitHub")
			}
		})
	}
}

func TestTrigger_ReservesGitHubProvenanceForTheWebhook(t *testing.T) {
	s, options, input, raw := sourceUploadFixture(t)
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	iteration := 0
	property := func(data string, appOrigin bool) bool {
		iteration++
		request := input
		request.Git.Repository.Display = data
		request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{
			Repository: "acme/repo",
			Event:      "push", DeliveryKey: data, InstallationID: 7, RepositoryID: 42,
			Ref: "refs/heads/main", SHA: input.Git.SHA, DefaultBranch: data,
			DefaultBranchPush: true, VerifiedAt: time.Now().UTC(),
		}}
		request.Git.DefaultBranch = runspec.Optional[string]{Set: true, Value: data}
		body := controllerwire.CreateRunRequest{IdempotencyKey: fmt.Sprintf("provenance-%d", iteration), Request: request}
		if appOrigin {
			body.Request.Origin = runspec.OriginApp
			payload, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(string(payload)))
			r.Header.Set("Authorization", "Bearer "+raw)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			return w.Code == http.StatusForbidden
		}
		created, err := c.CreateRun(t.Context(), body)
		if err != nil {
			t.Log(err)
			return false
		}
		got := created.Inputs
		return !got.Git.App.Set && !got.Git.DefaultBranch.Set && got.Git.Repository.Key == input.Git.Repository.Key
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

// Decision 68: only code the controller fetches through the team's GitHub App,
// as source selection resolves it, proves the repository for its pipeline
// secrets. Requests take the CLI's shape: an unqualified current branch with
// the locally resolved commits, and a configured source: pin.
func TestFetchedGitHubRepositoryFollowsSourceSelection(t *testing.T) {
	head, pinned, fork := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("f", 40)
	refs := map[string]string{"refs/heads/main": head, "refs/tags/v1": head, "refs/heads/release": pinned, "refs/heads/pinned": head}
	configs := map[string]string{head: "pipelines:\n  - name: ci\n    entrypoint: CI\n  - name: deploy\n    entrypoint: Deploy\n    source: release\n"}
	selectSource := func(ctx context.Context, request runspec.Request) (runspec.Git, runspec.Source, error) {
		git, code, _, err := source.Select(ctx, source.Spec{RepoKey: string(request.Git.Repository.Key), RemoteURL: request.Source.Location.RemoteURL}, request.Pipeline, request.Git, request.Source, "", source.Options{
			ResolveRef: func(_ context.Context, ref string) (string, error) { return refs[ref], nil },
			ReadFile: func(_ context.Context, sha, _ string) ([]byte, bool, error) {
				config, ok := configs[sha]
				return []byte(config), ok, nil
			},
		})
		if err != nil {
			return git, code, err
		}
		git, err = qualifyFetchedRef(ctx, git, func(_ context.Context, ref string) (string, error) { return refs[ref], nil })
		return git, code, err
	}
	cli := runspec.Request{Pipeline: "ci", Git: runspec.Git{Repository: runspec.Repository{Key: "gh:701"}, Ref: "main", SHA: head}, Source: runspec.Source{Ref: head, Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app"}}}
	app := &Server{githubApp: &githubAppState{}}
	// Decision 72: the claimed head is the branch or tag the App resolved, and
	// none when the code ran at a pinned branch's commit.
	for name, tc := range map[string]struct {
		change func(*runspec.Request)
		ref    string
	}{
		"unqualified current branch": {func(*runspec.Request) {}, "refs/heads/main"},
		"qualified branch":           {func(r *runspec.Request) { r.Git.Ref = "refs/heads/main" }, "refs/heads/main"},
		"unqualified tag":            {func(r *runspec.Request) { r.Git.Ref = "v1" }, "refs/tags/v1"},
		"branch, commit unnamed":     {func(r *runspec.Request) { r.Git.SHA, r.Source.Ref = "", "" }, "refs/heads/main"},
		"configured source pin": {func(r *runspec.Request) {
			r.Pipeline, r.Source.Ref, r.Source.RequestedRef = "deploy", pinned, "release"
		}, ""},
	} {
		request := cli
		tc.change(&request)
		ref := request.Git.Ref
		got, err := app.selectFetchedSource(t.Context(), &request, selectSource)
		if err != nil || got.ID != 701 || !strings.EqualFold(request.Git.SHA, head) || request.Git.Ref != ref || got.Ref != tc.ref || (got.SHA == head) != (tc.ref != "") {
			t.Errorf("%s = %+v %v %+v", name, got, err, request.Git)
		}
	}
	for name, change := range map[string]func(*runspec.Request){
		"fork commit on a branch":         func(r *runspec.Request) { r.Git.SHA, r.Source.Ref = fork, fork },
		"code at a commit no pin selects": func(r *runspec.Request) { r.Source.Ref = fork },
		"source pin bypassed":             func(r *runspec.Request) { r.Pipeline = "deploy" },
		"pull request ref":                func(r *runspec.Request) { r.Git.Ref = "refs/pull/7/head" },
		"bare commit":                     func(r *runspec.Request) { r.Git.Ref = fork },
		"no ref":                          func(r *runspec.Request) { r.Git.Ref = "" },
		"upload": func(r *runspec.Request) {
			r.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: "sha256:x"}
		},
		"working tree":   func(r *runspec.Request) { r.Source.WorkingTree = true },
		"local checkout": func(r *runspec.Request) { r.Source.Location.LocalCheckout = "/src" },
		"no remote":      func(r *runspec.Request) { r.Source.Location.RemoteURL = "" },
		"other host":     func(r *runspec.Request) { r.Git.Repository.Key = "git:git.example.com/acme/app" },
		"here": func(r *runspec.Request) {
			r.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
		},
	} {
		request := cli
		change(&request)
		if got, _ := app.selectFetchedSource(t.Context(), &request, selectSource); got.ID != 0 {
			t.Errorf("%s proved repository %+v", name, got)
		}
	}
	if got, _ := (&Server{}).selectFetchedSource(t.Context(), &cli, selectSource); got.ID != 0 {
		t.Fatalf("controller without the App proved repository %+v", got)
	}
}

// The console names only a repository and branch. Intake resolves that intent
// once through the App, admits the selected commit, and seals the repository
// that selection proved for pipeline secrets and OIDC repository claims
// (decision 70), with the owner/name GitHub reported, never the request's text,
// and the branch head it ran for the OIDC ref and sha claims (decision 72).
func TestConsoleTriggerAdmitsSelectedSourceAndProvesRepository(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("c", 40)
	s, session := credentialSessionServer(t)
	teststore.SeedGitHubInstallation(t, s.store, "a", store.GitHubAppInstallation{InstallationID: 7, AccountID: 70, AccountLogin: "acme", AccountType: "Organization", ConnectedBy: "member", GitHubUserID: 71}, time.Now())
	s.WithGitHubApp(githubapp.Config{AppID: 1, Slug: "test", ClientID: "id", ClientSecret: "secret", WebhookSecret: "webhook", PrivateKey: key, HTTP: &http.Client{Transport: intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		status, payload := 200, ""
		switch r.URL.Path {
		case "/repos/acme/app/installation":
			payload = `{"id":7,"app_id":1,"account":{"id":70,"login":"acme","type":"Organization"}}`
		case "/app/installations/7/access_tokens":
			payload = `{"token":"private","expires_at":"2100-01-01T00:00:00Z","permissions":{"metadata":"read","contents":"read"}}`
		case "/installation/repositories":
			payload = `{"total_count":1,"repositories":[{"id":42,"full_name":"acme/app","owner":{"id":70,"login":"acme"}}]}`
		case "/repos/acme/app/git/ref/heads/main":
			payload = `{"ref":"refs/heads/main","object":{"type":"commit","sha":"` + head + `"}}`
		case "/repos/acme/app/git/ref/tags/main", "/repos/acme/app/contents/.sparkwing/sparkwing.yaml":
			status, payload = 404, `{"message":"Not Found"}`
		default:
			return nil, fmt.Errorf("unexpected GitHub request %s", r.URL)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}})
	body := `{"idempotency_key":"console","request":{"version":1,"pipeline":"p","mode":"execute","origin":"trigger","dispatch":"host","requested_args":{},` +
		`"git":{"repository":{"display":"https://github.com/acme/app"},"ref":"main","repository_name":{"set":true,"value":"forged/name"}},` +
		`"source":{"ref":"main","location":{"remote_url":"https://github.com/acme/app","pipeline_dir":".sparkwing"}}}}`
	w := resumeHTTP(s, session, "/api/v1/runs", body)
	if w.Code != 201 {
		t.Fatalf("console trigger: HTTP %d %s", w.Code, w.Body)
	}
	var created controllerwire.CreateRunResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	got := run.Request
	if got.Request.Git.SHA != head || got.Request.Source.Ref != head || got.Provenance.Trust() != runspec.TrustAsserted || got.Provenance.SecretRepositoryID() != 42 {
		t.Fatalf("sealed git=%+v source ref=%q trust=%s proven=%d", got.Request.Git, got.Request.Source.Ref, got.Provenance.Trust(), got.Provenance.SecretRepositoryID())
	}
	if trigger, id, ref, sha := got.Provenance.OIDC(); trigger != "manual" || id != 42 || ref != "refs/heads/main" || sha != head || got.Provenance.Repository() != "acme/app" || got.Request.Git.Ref != "main" {
		t.Fatalf("OIDC identity = %s %d %q %q repository %q", trigger, id, ref, sha, got.Provenance.Repository())
	}
}
