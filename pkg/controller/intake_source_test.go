package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type intakeGitHubTransport func(*http.Request) (*http.Response, error)

func (f intakeGitHubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type intakeSourceTransaction struct {
	RunTransactions
	seen  store.IntakeRequest
	calls int
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
		tenant, err := s.store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.BindGitHubAppInstallation(t.Context(), store.GitHubAppInstallation{InstallationID: 7, AccountID: 70, AccountLogin: "acme", AccountType: "Organization", ConnectedBy: "member", GitHubUserID: 71}, time.Now()); err != nil {
			t.Fatal(err)
		}
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
		t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
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
