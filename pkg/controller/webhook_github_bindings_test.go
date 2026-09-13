package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const bindingSecret = "3b9d0a1f4c7e2b5a8d6f0c3e9b2a5d8f"

type bindingFixture struct {
	server *httptest.Server
	store  *store.Store
	admin  string
}

func newBindingFixture(t *testing.T, st *store.Store, configure func(*controller.Server) *controller.Server) *bindingFixture {
	t.Helper()
	admin, _, err := st.CreateToken("operator", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	srv := controller.New(st, nil).EnableAuthFromStore()
	if configure != nil {
		srv = configure(srv)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &bindingFixture{server: ts, store: st, admin: admin}
}

func (f *bindingFixture) connect(t *testing.T, req controller.GitHubWebhookBindingRequest) controller.GitHubWebhookBindingResponse {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal binding: %v", err)
	}
	resp, raw := f.do(t, http.MethodPost, "/api/v1/webhooks/github/bindings", body)
	if resp != http.StatusCreated {
		t.Fatalf("connect status = %d, body %s", resp, raw)
	}
	if strings.Contains(string(raw), req.Secret) {
		t.Fatalf("the connect response carries the secret: %s", raw)
	}
	var out controller.GitHubWebhookBindingResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode binding response: %v", err)
	}
	return out
}

func (f *bindingFixture) do(t *testing.T, method, path string, body []byte) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+f.admin)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, raw
}

func pushBody(repo, sha string) []byte {
	return []byte(fmt.Sprintf(
		`{"ref":"refs/heads/main","after":%q,"before":"0000000000000000000000000000000000000000",`+
			`"repository":{"full_name":%q},"pusher":{"name":"octocat"}}`, sha, repo))
}

func openSQLiteBindingStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// safety: each Postgres run gets a schema of its own, so a suite run beside
// another does not share the bindings table with it.
func openPostgresBindingStore(t *testing.T) *store.Store {
	t.Helper()
	base := os.Getenv("SPARKWING_TEST_PG_URL")
	if strings.TrimSpace(base) == "" {
		if os.Getenv("SPARKWING_REQUIRE_PG") != "" {
			t.Fatal("SPARKWING_REQUIRE_PG is set, so SPARKWING_TEST_PG_URL must name a reachable Postgres")
		}
		t.Skip("SPARKWING_TEST_PG_URL not set; skipping the Postgres dialect")
	}
	schema := "cw_" + strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '_'
	}, t.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := store.OpenPostgres(ctx, base)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if _, err := admin.DB().ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	_ = admin.Close()
	t.Cleanup(func() {
		cleanup, err := store.OpenPostgres(context.Background(), base)
		if err != nil {
			t.Errorf("open postgres to drop schema %s: %v", schema, err)
			return
		}
		if _, err := cleanup.DB().Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		_ = cleanup.Close()
	})
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	st, err := store.OpenPostgres(ctx, base+sep+"search_path="+schema)
	if err != nil {
		t.Fatalf("open postgres schema %s: %v", schema, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestGitHubWebhookBinding_RouteVerifiesDeliveriesOnBothDialects(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*testing.T) *store.Store
	}{
		{name: "sqlite", open: openSQLiteBindingStore},
		{name: "postgres", open: openPostgresBindingStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBindingFixture(t, tc.open(t), nil)
			bound := f.connect(t, controller.GitHubWebhookBindingRequest{
				Pipeline: "build", Repo: "Acme/Widgets", Secret: bindingSecret,
				Events: []string{"push", "pull_request"}, HookID: 4242,
			})
			if want := f.server.URL + "/webhooks/github/build"; bound.DeliveryURL != want {
				t.Errorf("delivery_url = %q, want %q", bound.DeliveryURL, want)
			}
			if bound.Repo != "acme/widgets" || bound.HookID != 4242 {
				t.Errorf("binding = %+v", bound)
			}

			body := pushBody("Acme/Widgets", "abc123")
			resp := postWebhookDelivery(t, bound.DeliveryURL, "push", "delivery-1",
				body, signWebhook(bindingSecret, body))
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("signed delivery status = %d, want 202", resp.StatusCode)
			}

			bad := postWebhookDelivery(t, bound.DeliveryURL, "push", "delivery-2",
				body, signWebhook("another-secret", body))
			defer func() { _ = bad.Body.Close() }()
			if bad.StatusCode != http.StatusUnauthorized {
				t.Errorf("delivery signed with another secret = %d, want 401", bad.StatusCode)
			}

			// safety: an unbound repository must not pass under the secret of
			// a repository that is bound.
			other := pushBody("acme/other", "def456")
			stranger := postWebhookDelivery(t, bound.DeliveryURL, "push", "delivery-3",
				other, signWebhook(bindingSecret, other))
			defer func() { _ = stranger.Body.Close() }()
			if stranger.StatusCode != http.StatusUnauthorized {
				t.Errorf("unbound repository = %d, want 401", stranger.StatusCode)
			}

			status, raw := f.do(t, http.MethodDelete,
				"/api/v1/webhooks/github/bindings?pipeline=build&repo=Acme%2FWidgets", nil)
			if status != http.StatusOK {
				t.Fatalf("disconnect status = %d, body %s", status, raw)
			}
			var removed controller.GitHubWebhookDisconnectResponse
			if err := json.Unmarshal(raw, &removed); err != nil {
				t.Fatalf("decode disconnect: %v", err)
			}
			if !removed.Removed {
				t.Error("disconnect reported no binding for one that was stored")
			}
			if removed.HookID != 4242 || removed.DeliveryURL != bound.DeliveryURL {
				t.Errorf("disconnect = %+v, want it to name the webhook it was bound to", removed)
			}

			after := postWebhookDelivery(t, bound.DeliveryURL, "push", "delivery-4",
				body, signWebhook(bindingSecret, body))
			defer func() { _ = after.Body.Close() }()
			if after.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("delivery after disconnect = %d, want 503 (no secret left)", after.StatusCode)
			}

			status, raw = f.do(t, http.MethodDelete,
				"/api/v1/webhooks/github/bindings?pipeline=build&repo=acme/widgets", nil)
			if status != http.StatusOK {
				t.Fatalf("second disconnect status = %d, body %s", status, raw)
			}
			if err := json.Unmarshal(raw, &removed); err != nil {
				t.Fatalf("decode second disconnect: %v", err)
			}
			if removed.Removed {
				t.Error("a second disconnect reported a binding it had already removed")
			}
		})
	}
}

func TestGitHubWebhookBinding_AnnouncedExternalURL(t *testing.T) {
	f := newBindingFixture(t, openSQLiteBindingStore(t), func(s *controller.Server) *controller.Server {
		return s.WithExternalURL("https://ci.example.dev/")
	})
	bound := f.connect(t, controller.GitHubWebhookBindingRequest{
		Pipeline: "build", Repo: "acme/widgets", Secret: bindingSecret,
	})
	if bound.DeliveryURL != "https://ci.example.dev/webhooks/github/build" {
		t.Errorf("delivery_url = %q, want the announced base", bound.DeliveryURL)
	}
}

// A stored binding adds to the document: the pipeline that document refuses
// every repository for still accepts the repository an operator connected.
func TestGitHubWebhookBinding_OverridesADenyAllDocument(t *testing.T) {
	f := newBindingFixture(t, openSQLiteBindingStore(t), func(s *controller.Server) *controller.Server {
		return s.WithGitHubWebhookConfig(controller.GitHubWebhookConfig{
			Pipelines: map[string]controller.GitHubWebhookBinding{
				"build": {Repos: []string{}},
			},
		})
	})
	bound := f.connect(t, controller.GitHubWebhookBindingRequest{
		Pipeline: "build", Repo: "acme/widgets", Secret: bindingSecret,
	})
	body := pushBody("acme/widgets", "abc123")
	resp := postWebhookDelivery(t, bound.DeliveryURL, "push", "deny-all-1",
		body, signWebhook(bindingSecret, body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want the connected repository to be accepted", resp.StatusCode)
	}
}

// Connecting one repository leaves a pipeline the document does not name
// open to the repositories the shared secret already covered.
func TestGitHubWebhookBinding_LeavesAnUncheckedPipelineUnchecked(t *testing.T) {
	f := newBindingFixture(t, openSQLiteBindingStore(t), func(s *controller.Server) *controller.Server {
		return s.WithGitHubWebhookSecret(testWebhookSecret)
	})
	f.connect(t, controller.GitHubWebhookBindingRequest{
		Pipeline: "build", Repo: "acme/widgets", Secret: bindingSecret,
	})
	body := pushBody("acme/legacy", "abc123")
	resp := postWebhookDelivery(t, f.server.URL+"/webhooks/github/build", "push", "legacy-1",
		body, signWebhook(testWebhookSecret, body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want the shared secret still accepted", resp.StatusCode)
	}
}

func TestGitHubWebhookBinding_RefusesMalformedRequests(t *testing.T) {
	f := newBindingFixture(t, openSQLiteBindingStore(t), nil)
	for name, req := range map[string]controller.GitHubWebhookBindingRequest{
		"no pipeline":     {Repo: "acme/widgets", Secret: bindingSecret},
		"no repo":         {Pipeline: "build", Secret: bindingSecret},
		"no secret":       {Pipeline: "build", Repo: "acme/widgets"},
		"repo not a slug": {Pipeline: "build", Repo: "widgets", Secret: bindingSecret},
		"pipeline path":   {Pipeline: "build/deploy", Repo: "acme/widgets", Secret: bindingSecret},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			status, raw := f.do(t, http.MethodPost, "/api/v1/webhooks/github/bindings", body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d (body %s), want 400", status, raw)
			}
		})
	}
}

func TestGitHubWebhookBinding_RequiresAdmin(t *testing.T) {
	st := openSQLiteBindingStore(t)
	f := newBindingFixture(t, st, nil)
	_, reader, err := st.CreateToken("viewer", store.TokenKindUser,
		[]string{controller.ScopeRunsRead}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	body, err := json.Marshal(controller.GitHubWebhookBindingRequest{
		Pipeline: "build", Repo: "acme/widgets", Secret: bindingSecret,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost,
		f.server.URL+"/api/v1/webhooks/github/bindings", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+reader.Prefix)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want the route to refuse a non-admin", resp.StatusCode)
	}
}
