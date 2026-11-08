package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestProductionWarmExchangesActionsIdentity(t *testing.T) {
	token := "ghs_123_" + strings.Repeat("aB1_-", 40) + "." + strings.Repeat("cD2_-", 40) + "." + strings.Repeat("eF3_-", 22)
	t.Setenv("GITHUB_TOKEN", token)
	expires := time.Now().Add(time.Hour)
	push := capability.PushScope{Receipt: "receipt", RepositoryKey: "gh:42", Repository: "acme/repo", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), WorkflowRunID: "123"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oidc":
			if r.Header.Get("Authorization") != "Bearer request-secret" || r.URL.Query().Get("audience") != "http://"+r.Host || r.URL.Query().Get("existing") != "kept" {
				t.Error("missing OIDC request authorization or audience")
			}
			json.NewEncoder(w).Encode(map[string]string{"value": "job-id-token"})
		case "/api/v1/runners/github/exchange":
			var request struct {
				Team    string `json:"team"`
				IDToken string `json:"id_token"`
				Receipt string `json:"receipt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Team != "acme" || request.IDToken != "job-id-token" || request.Receipt != "receipt" {
				t.Error("lost exchange proof")
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "runner-secret", "team": "acme", "expires_at": expires.Unix(), "binding": capability.Binding{Push: &push}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL+"/oidc?existing=kept")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-secret")
	cli := productionRuntime("v1.2.3").Runner
	boundary := errors.New("runtime boundary")
	cli.Factory = func(ctx context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
		raw, err := options.Bearer(ctx)
		if raw != "runner-secret" || err != nil || options.actions.repository != "gh:42" || options.Loop.ClaimUntil.Unix() != expires.Unix() || len(options.Loop.Kinds) != 1 || options.Loop.Kinds[0] != capability.UnitNode || options.Loop.MaxClaims != 1 {
			t.Fatal("lost server-issued Actions authority")
		}
		credential, err := options.actions.GitCredential(ctx, source.Spec{RepoKey: "gh:42", RemoteURL: "https://github.com/acme/repo.git"})
		if err != nil || credential.Secret != token || credential.Username != "x-access-token" {
			t.Fatalf("Actions source did not preserve its installation token: %v", err)
		}
		return runners.RuntimeConfig{}, boundary
	}
	err := cli.Run(t.Context(), "warm", []string{"--controller=" + server.URL, "--github-actions", "--dispatch-receipt=receipt", "--team=acme"}, "v1.2.3")
	if !errors.Is(err, boundary) {
		t.Fatalf("Run = %v", err)
	}
}
