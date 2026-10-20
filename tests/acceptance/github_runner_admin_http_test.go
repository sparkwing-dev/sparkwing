package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type githubRunnerRegistry struct {
	team          store.Team
	rows          []store.GitHubRunnerBinding
	actor         capability.Authorized
	written       store.GitHubRunnerBinding
	removed       int64
	reads, writes int
	err           error
}

func (f *githubRunnerRegistry) Team() store.Team { return f.team }
func (f *githubRunnerRegistry) GitHubRunnerBindings(context.Context) ([]store.GitHubRunnerBinding, error) {
	f.reads++
	return f.rows, f.err
}

func (f *githubRunnerRegistry) RegisterGitHubRunner(_ context.Context, a capability.Authorized, b store.GitHubRunnerBinding) (store.GitHubRunnerBinding, error) {
	f.actor, f.written = a, b
	f.writes++
	b.CreatedAt = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	return b, f.err
}

func (f *githubRunnerRegistry) RevokeGitHubRunner(_ context.Context, a capability.Authorized, id int64) error {
	f.actor, f.removed = a, id
	f.writes++
	return f.err
}

func githubRunnerAdminServer(t testing.TB, b *credentialBoundary, f *githubRunnerRegistry) *controller.Server {
	t.Helper()
	return controller.NewDeployment(nil, deploymenttest.Local(t, "https://controller.invalid"), slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithAuthorization(consoleAuthorization(b)).WithGitHubRunnerRegistry(func(_ context.Context, team store.Team) (controller.GitHubRunnerRegistry, error) {
		if team != "team" {
			t.Fatalf("resolved foreign tenant %q", team)
		}
		return f, nil
	})
}

func TestGitHubRunnerAdminHTTPAuthorityProperty(t *testing.T) {
	property := func(selector string, repository uint32) bool {
		b, f := authorityMember(), &githubRunnerRegistry{team: "team"}
		h := githubRunnerAdminServer(t, b, f).Handler()
		body := `{"repository":"acme/widgets","repository_id":42,"repository_owner_id":7}`
		query := url.Values{"team": {selector}, "account": {selector}, "repository_id": {selector}}.Encode()
		w := authorityHTTP(h, b, "POST", "/api/v1/runners/github?"+query, body)
		if w.Code != 201 || f.writes != 1 || f.actor.Principal.Authority != b.snapshot.Authorities[0].Principal.Authority || f.actor.Target.Team != "team" || f.written.Team != "team" || f.written.CreatedBy != b.snapshot.Authorities[0].Principal.Capability.Subject.ID || f.written.RepositoryID != 42 || f.written.RepositoryOwnerID != 7 || w.Header().Get("Cache-Control") != "no-store" {
			return false
		}
		id := int64(repository) + 1
		path := "/api/v1/runners/github/" + strconv.FormatInt(id, 10) + "?" + query
		w = authorityHTTP(h, b, "DELETE", path, "")
		return w.Code == 204 && f.writes == 2 && f.removed == id && f.actor.Target.Team == "team"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubRunnerAdminHTTPRefusals(t *testing.T) {
	for _, method := range []string{"GET", "POST", "DELETE"} {
		for _, name := range []string{"missing", "revoked", "expiry", "membership", "grant", "machine", "claim", "foreign", "reader", "race", "private", "missing adapter"} {
			t.Run(method+"/"+name, func(t *testing.T) {
				b, f := authorityMember(), &githubRunnerRegistry{team: "team"}
				p := &b.snapshot.Authorities[0].Principal
				want := apierr.Forbidden
				calls := 0
				switch name {
				case "missing":
					b.raw, want = "", apierr.Unauthenticated
				case "revoked":
					p.Capability.RevokedAt, want = &b.snapshot.Now, apierr.Unauthenticated
				case "expiry":
					p.Capability.ExpiresAt, want = &b.snapshot.Now, apierr.Unauthenticated
				case "membership":
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt, want = &b.snapshot.Now, apierr.Unauthenticated
				case "grant":
					p.Capability.Grants = nil
				case "machine":
					p.Capability.Subject.Kind = capability.SubjectMachine
				case "claim":
					b = newCredentialBoundary()
					b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.TeamAdmin, capability.RunsRead}
				case "foreign":
					f.team, want = "foreign", apierr.PolicyMissing
				case "reader":
					p.Capability.Grants = []capability.Grant{capability.RunsRead}
					if method == "GET" {
						want, calls = "", 1
					}
				case "race":
					f.err, want, calls = &apierr.Error{Code: apierr.LockHeld, Message: "authority changed"}, apierr.LockHeld, 1
				case "private":
					f.err, want, calls = errors.New("private provider credential"), apierr.Internal, 1
				case "missing adapter":
					want = apierr.PolicyMissing
				}
				s := githubRunnerAdminServer(t, b, f)
				if name == "missing adapter" {
					s.WithGitHubRunnerRegistry(nil)
				}
				path := "/api/v1/runners/github"
				if method == "DELETE" {
					path += "/42"
				}
				w := authorityHTTP(s.Handler(), b, method, path, `{"repository":"acme/widgets","repository_id":42,"repository_owner_id":7}`)
				if want == "" {
					if w.Code != 200 || !strings.Contains(w.Body.String(), `"bindings":[]`) {
						t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
					}
				} else {
					assertEngineError(t, w, apierr.Status(want), want)
				}
				if f.reads+f.writes != calls || strings.Contains(w.Body.String(), "private") {
					t.Fatalf("HTTP %d %s reads=%d writes=%d", w.Code, w.Body.String(), f.reads, f.writes)
				}
			})
		}
	}
}

func TestGitHubRunnerAdminHTTPInputAndMethods(t *testing.T) {
	b, f := authorityMember(), &githubRunnerRegistry{team: "team"}
	h := githubRunnerAdminServer(t, b, f).Handler()
	for _, body := range []string{`null`, `{}`, `{"repository":"https://github.com/acme/widgets","repository_id":42,"repository_owner_id":7}`, `{"repository":"acme/widgets","repository_id":-1,"repository_owner_id":7}`, `{"repository":"acme/widgets","repository_id":42}`, `{"repository":"acme/widgets","repository_id":42,"repository_owner_id":7,"created_by":"forged"}`, `{"repository":"acme/widgets","repository_id":42,"repository_owner_id":7,"team":"foreign"}`} {
		assertEngineError(t, authorityHTTP(h, b, "POST", "/api/v1/runners/github", body), 400, apierr.BadRequest)
	}
	for _, id := range []string{"0", "-1", "+42", "042", "invalid", "9223372036854775808"} {
		assertEngineError(t, authorityHTTP(h, b, "DELETE", "/api/v1/runners/github/"+url.PathEscape(id), ""), 400, apierr.BadRequest)
	}
	for _, path := range []string{"/api/v1/runners/github", "/api/v1/runners/github/42"} {
		assertEngineError(t, authorityHTTP(h, b, "HEAD", path, ""), 405, apierr.MethodNotAllowed)
	}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		path := "/api/v1/team/github-runners"
		if method == "DELETE" {
			path += "/42"
		}
		if githubRunnerAdminServer(t, b, f).RegisteredRoute(httptest.NewRequest(method, path, nil)) {
			t.Fatal("retired route remains registered")
		}
	}
	if f.writes != 0 || f.reads != 0 {
		t.Fatal("invalid request reached persistence")
	}
}

func TestGitHubRunnerAdminHTTPEmptyStoreAndMissingWriter(t *testing.T) {
	st, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "bindings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, team := range []store.Team{"team", "foreign"} {
		teststore.SeedTeam(t, st, team)
	}
	b := authorityMember()
	h := controller.NewDeployment(st, deploymenttest.Local(t, "https://controller.invalid"), slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).Handler()
	w := authorityHTTP(h, b, "GET", "/api/v1/runners/github?team=foreign", "")
	var result struct {
		Bindings []struct {
			Repository string `json:"repository"`
		}
		Workflow string
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Bindings == nil || len(result.Bindings) != 0 || !strings.Contains(result.Workflow, "--team team") {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	h = controller.NewDeployment(st, deploymenttest.Local(t, "https://controller.invalid"), slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).WithGitHubRunnerRegistry(func(context.Context, store.Team) (controller.GitHubRunnerRegistry, error) { return nil, nil }).Handler()
	assertEngineError(t, authorityHTTP(h, b, "POST", "/api/v1/runners/github", `{"repository":"acme/widgets","repository_id":43,"repository_owner_id":7}`), 500, apierr.PolicyMissing)
	assertEngineError(t, authorityHTTP(h, b, "DELETE", "/api/v1/runners/github/42", ""), 500, apierr.PolicyMissing)
}

func TestGitHubRunnerAdminHTTPInventoryMetadata(t *testing.T) {
	b := authorityMember()
	f := &githubRunnerRegistry{team: "team", rows: []store.GitHubRunnerBinding{{Team: "team", Repository: "acme/widgets", RepositoryID: 42, RepositoryOwnerID: 7, CreatedBy: "creator", CreatedAt: b.snapshot.Now}}}
	w := authorityHTTP(githubRunnerAdminServer(t, b, f).Handler(), b, "GET", "/api/v1/runners/github", "")
	var result struct {
		Bindings []map[string]json.RawMessage `json:"bindings"`
		Workflow string                       `json:"workflow"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Bindings) != 1 || len(result.Bindings[0]) != 5 || string(result.Bindings[0]["repository_id"]) != "42" || !strings.Contains(result.Workflow, "--team team") || !strings.Contains(result.Workflow, "--controller https://controller.invalid") {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	f.rows[0].Team = "foreign"
	assertEngineError(t, authorityHTTP(githubRunnerAdminServer(t, b, f).Handler(), b, "GET", "/api/v1/runners/github", ""), 500, apierr.Internal)
}

func TestGitHubRunnerAdminHTTPSessionCSRF(t *testing.T) {
	for _, method := range []string{"POST", "DELETE"} {
		b, f := authorityMember(), &githubRunnerRegistry{team: "team"}
		p := &b.snapshot.Authorities[0].Principal
		p.Class, p.Authority = capability.CredentialSession, sessionReference(*p)
		h := githubRunnerAdminServer(t, b, f).Handler()
		path := "/api/v1/runners/github"
		if method == "DELETE" {
			path += "/42"
		}
		body := `{"repository":"acme/widgets","repository_id":42,"repository_owner_id":7}`
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assertEngineError(t, w, 403, apierr.Forbidden)
		if f.writes != 0 {
			t.Fatal("missing CSRF reached transaction")
		}
		w = authorityHTTP(h, b, method, path, body)
		want := 201
		if method == "DELETE" {
			want = 204
		}
		if w.Code != want || f.writes != 1 {
			t.Fatalf("HTTP %d %s writes=%d", w.Code, w.Body.String(), f.writes)
		}
	}
}
