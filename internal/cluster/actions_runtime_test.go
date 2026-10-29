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
)

func TestProductionWarmExchangesActionsIdentity(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	push := capability.PushScope{RepositoryKey: "gh:42", Repository: "acme/repo", Ref: "refs/heads/main", SHA: strings.Repeat("a", 40), WorkflowRunID: "123"}
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
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Team != "acme" || request.IDToken != "job-id-token" {
				t.Error("lost exchange proof")
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "runner-secret", "team": "acme", "expires_at": expires.Unix(), "binding": capability.Binding{Push: &push}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", server.URL+"/oidc?existing=kept")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-secret")
	cli := productionRuntime("v1.2.3").Runner
	boundary := errors.New("runtime boundary")
	cli.Factory = func(ctx context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
		raw, err := options.Bearer(ctx)
		if raw != "runner-secret" || err != nil || options.actions.repository != "gh:42" || options.Loop.ClaimUntil.Unix() != expires.Unix() {
			t.Fatal("lost server-issued Actions authority")
		}
		return runners.RuntimeConfig{}, boundary
	}
	err := cli.Run(t.Context(), "warm", []string{"--controller=" + server.URL, "--github-actions", "--team=acme"}, "v1.2.3")
	if !errors.Is(err, boundary) {
		t.Fatalf("Run = %v", err)
	}
}
