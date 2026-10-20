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
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type intakeGitHubTransport func(*http.Request) (*http.Response, error)

func (f intakeGitHubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type intakeSourceTransaction struct {
	RunTransactions
	seen  store.IntakeRequest
	calls int
}

func (f *intakeSourceTransaction) ReplayRun(context.Context, store.IntakeRequest) (store.AdmissionResult, bool, error) {
	return store.AdmissionResult{}, false, nil
}

func (f *intakeSourceTransaction) AdmitRun(_ context.Context, input store.IntakeRequest) (store.AdmissionResult, error) {
	f.seen = input
	f.calls++
	return store.AdmissionResult{RunID: "created", Request: runspec.SealedRequest{Request: input.Request}}, nil
}

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
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
		})}}
		s.WithGitHubApp(cfg)
		transaction := &intakeSourceTransaction{}
		s.WithEngineController(&EngineController{Store: s.store, ResolveTenant: func(context.Context, store.Team) (RunTransactions, error) { return transaction, nil }})
		request := runspec.Request{Version: 1, Pipeline: "p", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Display: input}, Ref: "main"}, Source: runspec.Source{Ref: "main", Location: runspec.SourceLocation{RemoteURL: "https://github.com/acme/app.git", PipelineDir: ".sparkwing"}}}
		if forged {
			request.Git.Repository.Key = "gh:43"
		}
		body, err := json.Marshal(map[string]any{"idempotency_key": "request", "request": request})
		if err != nil {
			t.Fatal(err)
		}
		w := resumeHTTP(s, session, "/api/v1/runs", string(body))
		if forged {
			return w.Code == 403 && transaction.calls == 0
		}
		got := transaction.seen.Request
		if w.Code != 201 || transaction.calls != 1 || got.Team != "a" || got.Git.Repository.Key != "gh:42" || got.Source.Location.RemoteURL != request.Source.Location.RemoteURL || got.Git.App.Set {
			t.Logf("HTTP %d: %s request=%+v", w.Code, w.Body, got)
			return false
		}
		return transaction.seen.Authority.Kind == "session" && transaction.seen.IdempotencyKey == "request"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestIntakeSourceRefusesUnverifiedAndCredentialBearingLocations(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = intakeGitHubTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Header: make(http.Header), Body: http.NoBody}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	e, _ := engineRuntimeStore(t)
	s := New(e.Store, nil)
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
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":42}`))}, nil
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
	if err != nil || created.Request.Request.Git.Repository.Key != "gh:42" || created.Request.Request.Git.App.Set || calls < 1 {
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
	sealed, err := sealSecret(cipher, gitCredentialBinding(store.GitCredential{Team: "a", Host: "github.com", HostWide: true}), "team-read-token")
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `INSERT INTO git_credentials(host_wide,id,team,host,kind,username,secret,confirmed_at,created_at,updated_at) VALUES (1,'source','a','github.com','https','oauth',?,1,1,1)`, sealed)
	original := http.DefaultTransport
	http.DefaultTransport = intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.github.com/repos/acme/app" || r.Header.Get("Authorization") != "Bearer team-read-token" {
			t.Fatal("private identity escaped the configured host credential")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":42}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	input.Git.Repository = runspec.Repository{Key: "gh:42", Display: "acme/app"}
	input.Source.Location.RemoteURL = "https://github.com/acme/app.git"
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{IdempotencyKey: "private-github", Request: input})
	if err != nil || created.Request.Request.Git.Repository.Key != "gh:42" || created.Request.Request.Git.App.Set {
		t.Fatalf("private GitHub intake: %+v %v", created, err)
	}
	raw, err := json.Marshal(created)
	if err != nil || strings.Contains(string(raw), "team-read-token") {
		t.Fatal("source credential escaped into the receipt")
	}
}

func TestIntakeSourceProofCannotBeChangedProperty(t *testing.T) {
	s, _, input, raw := sourceUploadFixture(t)
	p, err := s.store.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
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
	parent, err := s.store.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := s.store.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: parent.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.RunsWrite}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	http.DefaultTransport = intakeGitHubTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":42}`))}, nil
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
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":42}`))}, nil
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
				if created.Request.Request.Git.Repository.Key != "gh:42" || created.Request.Request.Git.App.Set || calls <= before {
					t.Fatalf("unverified repository: %+v, calls=%d", created.Request.Request.Git, calls-before)
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
			Event: "push", DeliveryKey: data, InstallationID: 7, RepositoryID: 42,
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
			r := httptest.NewRequest("POST", "/api/v1/runs", strings.NewReader(string(payload)))
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
		got := created.Request.Request
		return !got.Git.App.Set && !got.Git.DefaultBranch.Set && got.Origin == runspec.OriginTrigger && got.Git.Repository.Key == input.Git.Repository.Key
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
