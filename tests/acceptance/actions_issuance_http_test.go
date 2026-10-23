package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/internal/jwks/jwkstest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

type actionsExchangeFixture struct {
	signer *jwkstest.Signer
	server *controller.Server
	issuer *credentialIssuer
	claims map[string]any
	lookup func(context.Context, store.Team, int64) (store.GitHubRunnerBinding, error)
	calls  int
	logs   strings.Builder
}

func newActionsExchangeFixture(t testing.TB) *actionsExchangeFixture {
	t.Helper()
	f := &actionsExchangeFixture{signer: jwkstest.NewSigner(t, "actions-key"), issuer: &credentialIssuer{}}
	now := time.Now().UTC().Truncate(time.Second)
	f.claims = map[string]any{
		"iss": "https://actions.test", "aud": "https://controller.test", "sub": "repo:acme/widgets:ref:refs/heads/main",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"repository": "acme/widgets", "repository_id": "42", "repository_owner_id": "7",
		"ref": "refs/heads/main", "sha": strings.Repeat("a", 40), "event_name": "push",
		"workflow_ref": "acme/widgets/.github/workflows/ci.yaml@refs/heads/main", "run_id": "1000", "run_attempt": "1",
	}
	f.lookup = func(_ context.Context, team store.Team, id int64) (store.GitHubRunnerBinding, error) {
		return store.GitHubRunnerBinding{Team: team, RepositoryID: id, RepositoryOwnerID: 7, Repository: "acme/widgets"}, nil
	}
	f.server = controller.New(nil, slog.New(slog.NewTextHandler(&f.logs, nil))).WithCredentialIssuer(f.issuer).
		WithGitHubRunners(githuboidc.Config{
			Audience: "https://controller.test", Issuer: "https://actions.test", JWKSURL: "https://actions.test/keys",
			Now: func() time.Time { return now }, HTTP: &http.Client{Transport: reportTransport{http.HandlerFunc(f.signer.ServeKeys)}},
		}).WithGitHubRunnerBindingLookup(func(ctx context.Context, team store.Team, id int64) (store.GitHubRunnerBinding, error) {
		f.calls++
		return f.lookup(ctx, team, id)
	})
	return f
}

func (f *actionsExchangeFixture) token(t testing.TB) string {
	t.Helper()
	raw, err := f.signer.Sign(f.claims)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *actionsExchangeFixture) request(t testing.TB, method, token, team string, extra map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"id_token": token, "team": team}
	for key, value := range extra {
		body[key] = value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "https://controller.test/api/v1/runners/github/exchange?team=foreign", strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer caller-selected-operator")
	r.Header.Set("X-Sparkwing-Team", "foreign")
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	return w
}

func TestActionsIssuanceHTTPUsesVerifiedOrigin(t *testing.T) {
	for _, event := range []string{"push", "workflow_dispatch", "schedule"} {
		t.Run(event, func(t *testing.T) {
			f := newActionsExchangeFixture(t)
			issuedExpiry := time.Unix(f.claims["exp"].(int64), 0).Add(15 * time.Minute)
			f.issuer.expires = &issuedExpiry
			f.claims["event_name"] = event
			w := f.request(t, "POST", f.token(t), "team", nil)
			var result struct {
				Token, Team, Repository string
				ExpiresAt               int64 `json:"expires_at"`
				Labels                  []string
			}
			if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Token != "one-time-private-bearer" || result.Team != "team" || result.Repository != "acme/widgets" || !reflect.DeepEqual(result.Labels, []string{controller.GitHubActionsLabel}) {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			if len(f.issuer.provisions) != 1 || len(f.issuer.delegations) != 0 || f.calls != 1 {
				t.Fatalf("issuer=%+v lookups=%d", f.issuer, f.calls)
			}
			spec := f.issuer.provisions[0]
			want := capability.Binding{Kind: capability.BindingPush, Push: &capability.PushScope{RepositoryKey: "gh:42", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), WorkflowRunID: "1000", WorkflowRunAttempt: "1", WorkflowRef: "acme/widgets/.github/workflows/ci.yaml@refs/heads/main"}}
			if spec.Kind != capability.ProvisionActions || spec.Team != "team" || spec.Subject.Kind != capability.SubjectMachine || spec.Subject.ID == "" || spec.RegistrationID == "" || spec.Authority != nil || spec.Metered || spec.Data != nil || len(spec.ReservedPools) != 0 || !reflect.DeepEqual(spec.Repositories, []string{"gh:42"}) || !reflect.DeepEqual(spec.Binding, want) || !reflect.DeepEqual(spec.Grants, []capability.Grant{capability.NodesClaim, capability.TriggersClaim}) {
				t.Fatalf("spec=%+v", spec)
			}
			if spec.ExpiresAt == nil || !spec.ExpiresAt.Equal(time.Unix(f.claims["exp"].(int64), 0).Add(time.Hour)) || result.ExpiresAt != issuedExpiry.Unix() || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("expiry/headers=%v %v", spec.ExpiresAt, w.Header())
			}
		})
	}
}

func TestActionsIssuanceHTTPRefusesInvalidProofAndConsent(t *testing.T) {
	for _, failure := range []string{"issuer", "audience", "expired", "future", "signature", "foreign key", "no token", "no team", "repository", "repository id", "owner id", "transfer", "wrong binding id", "wrong binding team", "missing binding", "unknown team", "lookup error", "pull request", "pull request on branch", "pull request target", "pull request target on branch", "workflow run", "tag", "no branch", "sha", "no sha", "no run", "no workflow", "transaction", "private issuer error", "missing issuer", "missing verifier", "missing lookup", "keys unavailable"} {
		t.Run(failure, func(t *testing.T) {
			f := newActionsExchangeFixture(t)
			status, code, issued := 403, apierr.Forbidden, 0
			team := "team"
			switch failure {
			case "issuer":
				f.claims["iss"], status, code = "https://foreign.test", 401, apierr.Unauthenticated
			case "audience":
				f.claims["aud"], status, code = "https://foreign.test", 401, apierr.Unauthenticated
			case "expired":
				f.claims["exp"], status, code = int64(1), 401, apierr.Unauthenticated
			case "future":
				f.claims["nbf"], status, code = time.Now().Add(time.Hour).Unix(), 401, apierr.Unauthenticated
			case "signature", "foreign key", "no token", "repository", "repository id", "owner id":
				status, code = 401, apierr.Unauthenticated
				if failure == "repository" {
					f.claims["repository"] = "invalid"
				}
				if failure == "repository id" {
					f.claims["repository_id"] = "0"
				}
				if failure == "owner id" {
					f.claims["repository_owner_id"] = "0"
				}
			case "no team":
				team = ""
			case "transfer":
				f.claims["repository_owner_id"] = "8"
			case "wrong binding id":
				f.lookup = func(context.Context, store.Team, int64) (store.GitHubRunnerBinding, error) {
					return store.GitHubRunnerBinding{Team: "team", RepositoryID: 43, RepositoryOwnerID: 7}, nil
				}
			case "wrong binding team":
				f.lookup = func(context.Context, store.Team, int64) (store.GitHubRunnerBinding, error) {
					return store.GitHubRunnerBinding{Team: "foreign", RepositoryID: 42, RepositoryOwnerID: 7}, nil
				}
			case "missing binding", "unknown team", "lookup error":
				err := store.ErrNotFound
				if failure == "unknown team" {
					err = store.ErrUnknownTeam
				}
				if failure == "lookup error" {
					err, status, code = errors.New("private lookup detail"), 500, apierr.Internal
				}
				f.lookup = func(context.Context, store.Team, int64) (store.GitHubRunnerBinding, error) {
					return store.GitHubRunnerBinding{}, err
				}
			case "pull request":
				f.claims["event_name"], f.claims["ref"] = "pull_request", "refs/pull/7/merge"
			case "pull request on branch":
				f.claims["event_name"] = "pull_request"
			case "pull request target":
				f.claims["event_name"], f.claims["ref"] = "pull_request_target", "refs/pull/7/merge"
			case "pull request target on branch":
				f.claims["event_name"] = "pull_request_target"
			case "workflow run":
				f.claims["event_name"] = "workflow_run"
			case "tag":
				f.claims["ref"] = "refs/tags/v1"
			case "no branch":
				f.claims["ref"] = "refs/heads/"
			case "sha":
				f.claims["sha"] = "not-a-commit"
			case "no sha":
				delete(f.claims, "sha")
			case "no run":
				delete(f.claims, "run_id")
			case "no workflow":
				delete(f.claims, "workflow_ref")
			case "transaction":
				f.issuer.err, issued = &apierr.Error{Code: apierr.Forbidden, Message: "registration removed"}, 1
			case "private issuer error":
				f.issuer.err, issued, status, code = errors.New("private issuer detail"), 1, 500, apierr.Internal
			case "missing issuer":
				f.server.WithCredentialIssuer(nil)
				status, code = 500, apierr.PolicyMissing
			case "missing verifier":
				f.server = controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithCredentialIssuer(f.issuer)
				status, code = 503, apierr.Unavailable
			case "missing lookup":
				f.server.WithGitHubRunnerBindingLookup(nil)
				status, code = 500, apierr.PolicyMissing
			case "keys unavailable":
				f.server.WithGitHubRunners(githuboidc.Config{
					Audience: "https://controller.test", Issuer: "https://actions.test", JWKSURL: "https://actions.test/keys",
					HTTP: &http.Client{Transport: reportTransport{http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) })}},
				})
				status, code = 503, apierr.Unavailable
			}
			token := f.token(t)
			if failure == "no token" {
				token = ""
			}
			if failure == "signature" {
				token += "corrupted"
			}
			if failure == "foreign key" {
				foreign := jwkstest.NewSigner(t, "foreign")
				var err error
				token, err = f.signer.SignWith(foreign.Key, f.claims)
				if err != nil {
					t.Fatal(err)
				}
			}
			w := f.request(t, "POST", token, team, nil)
			assertEngineError(t, w, status, code)
			if len(f.issuer.provisions) != issued || strings.Contains(w.Body.String()+f.logs.String(), token) && token != "" || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "one-time-private-bearer") {
				t.Fatalf("issuer=%+v logs=%s", f.issuer, f.logs.String())
			}
		})
	}
}

func TestActionsIssuanceHTTPCallerFieldsCannotSelectAuthority(t *testing.T) {
	f := newActionsExchangeFixture(t)
	token := f.token(t)
	property := func(input string, field uint8) bool {
		fields := []string{"grants", "subject", "authority", "registration_id", "binding", "reserved_pools", "metered", "expires_at", "repositories", "sha", "ref", "repository_id", "repository_owner_id"}
		w := f.request(t, "POST", token, "team", map[string]any{fields[int(field)%len(fields)]: input})
		return w.Code == 400 && len(f.issuer.provisions) == 0 && f.calls == 0
	}
	for i := range uint8(13) {
		if !property("operator", i) {
			t.Fatalf("field %d accepted", i)
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD", "PUT"} {
		t.Run(method, func(t *testing.T) {
			w := f.request(t, method, token, "team", nil)
			assertEngineError(t, w, 405, apierr.MethodNotAllowed)
		})
	}
	session := newSessionFlow(t)
	if _, err := teststore.SQL(t, session.store).ExecContext(t.Context(), `UPDATE memberships SET role='owner'`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	session.server.Handler().ServeHTTP(w, session.request("DELETE", "/api/v1/runners/github/exchange", ""))
	assertEngineError(t, w, 400, apierr.BadRequest)
}

func TestActionsIssuanceHTTPMalformedBodyCannotReachOrigin(t *testing.T) {
	f := newActionsExchangeFixture(t)
	for _, body := range []string{"", "null", "{}", `{"id_token":null,"team":"team"}`, `{"id_token":[],"team":"team"}`, `{"id_token":"private","team":1}`, `{"id_token":"private","team":"team"} {}`} {
		r := httptest.NewRequest("POST", "/api/v1/runners/github/exchange", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(w, r)
		if w.Code != 400 && w.Code != 401 || f.calls != 0 || len(f.issuer.provisions) != 0 || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("malformed input HTTP %d %s", w.Code, w.Body.String())
		}
	}
}

func TestActionsIssuanceHTTPBindingCannotCrossTeamRepositoryOrOwner(t *testing.T) {
	f := newActionsExchangeFixture(t)
	f.lookup = func(context.Context, store.Team, int64) (store.GitHubRunnerBinding, error) {
		return store.GitHubRunnerBinding{Team: "team", RepositoryID: 42, RepositoryOwnerID: 7}, nil
	}
	property := func(repo, owner uint8, sameTeam bool) bool {
		f.issuer.provisions = nil
		f.claims["repository_id"] = strconv.Itoa(42 + int(repo%3))
		f.claims["repository_owner_id"] = strconv.Itoa(7 + int(owner%3))
		team := "foreign"
		if sameTeam {
			team = "team"
		}
		w := f.request(t, "POST", f.token(t), team, nil)
		if repo%3 == 0 && owner%3 == 0 && sameTeam {
			return w.Code == 201 && len(f.issuer.provisions) == 1
		}
		return w.Code == 403 && len(f.issuer.provisions) == 0 && !strings.Contains(w.Body.String(), "one-time-private-bearer")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96}); err != nil {
		t.Fatal(err)
	}
}

func TestActionsIssuanceHTTPCommitsThroughSoleIssuer(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, query := range []string{
		`INSERT INTO teams(name,created_at,updated_at) VALUES ('team',1,1)`,
		`INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,created_at,repo_key) VALUES ('team',42,7,'acme/widgets',1,'gh:42')`,
	} {
		if _, err := teststore.SQL(t, st).ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	f := newActionsExchangeFixture(t)
	f.server = controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).WithGitHubRunners(githuboidc.Config{
		Audience: "https://controller.test", Issuer: "https://actions.test", JWKSURL: "https://actions.test/keys",
		HTTP: &http.Client{Transport: reportTransport{http.HandlerFunc(f.signer.ServeKeys)}},
	})
	w := f.request(t, "POST", f.token(t), "team", nil)
	var result struct{ Token string }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Token == "" {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	principal, err := st.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(result.Token)))
	if err != nil || principal.Capability.Parent != nil || principal.Capability.Binding.Push == nil || principal.Capability.Binding.Push.RepositoryKey != "gh:42" || principal.Capability.Binding.Push.WorkflowRunID != "1000" || principal.Capability.Subject.ID != "github:42:7:1000:1" || principal.Capability.ExpiresAt == nil || principal.Capability.ExpiresAt.Sub(principal.Capability.IssuedAt) <= 0 || principal.Capability.ExpiresAt.Sub(principal.Capability.IssuedAt) > time.Hour || !reflect.DeepEqual(principal.Capability.Grants, []capability.Grant{capability.NodesClaim, capability.TriggersClaim}) {
		t.Fatalf("persisted root=%+v err=%v", principal, err)
	}
	for path, status := range map[string]int{"/api/v1/auth/whoami": 200, "/api/v1/pipelines/build/sizing": 403} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+result.Token)
		w := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("root %s HTTP %d %s", path, w.Code, w.Body.String())
		}
	}
	var count int
	if err := teststore.SQL(t, st).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities WHERE team='team' AND bind_kind='push'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("capabilities=%d err=%v", count, err)
	}
	if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `DELETE FROM github_runner_bindings WHERE team='team' AND repository_id=42`); err != nil {
		t.Fatal(err)
	}
	w = f.request(t, "POST", f.token(t), "team", nil)
	assertEngineError(t, w, 403, apierr.Forbidden)
	if err := teststore.SQL(t, st).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed issuance persisted: capabilities=%d err=%v", count, err)
	}
}
