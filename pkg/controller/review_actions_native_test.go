package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/internal/jwks/jwkstest"
)

func TestReviewNativeActionsRootIssuance(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	engineSQL(t, e.Store, `INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,created_by,created_at,repo_key) VALUES ('a',42,7,'acme/repo','member',1,'gh:42')`)
	signer := jwkstest.NewSigner(t, "actions-key")
	client := &http.Client{Transport: intakeGitHubTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method != "GET" || request.URL.String() != githuboidc.JWKSURL {
			return nil, fmt.Errorf("unexpected key request %s %s", request.Method, request.URL)
		}
		response := httptest.NewRecorder()
		signer.ServeKeys(response, request)
		return response.Result(), nil
	})}
	s := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithGitHubRunners(githuboidc.Config{Audience: "https://controller.test", Issuer: githuboidc.Issuer, JWKSURL: githuboidc.JWKSURL, HTTP: client})
	now := time.Now()
	claims := map[string]any{
		"iss": githuboidc.Issuer, "aud": "https://controller.test", "sub": "repo:acme/repo:ref:refs/heads/main",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"repository": "acme/repo", "repository_id": "42", "repository_owner_id": "7",
		"ref": "refs/heads/main", "sha": strings.Repeat("a", 40), "event_name": "push",
		"workflow_ref": "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main", "run_id": "1000",
	}
	sign := func() string {
		token, err := signer.Sign(claims)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	exchange := func(token string, status int) *httptest.ResponseRecorder {
		body, err := json.Marshal(githubExchangeReq{IDToken: token, Team: "a"})
		if err != nil {
			t.Fatal(err)
		}
		return engineClaimHTTP(t, s.Handler(), "", "POST", "/api/v1/runners/github/exchange", string(body), status)
	}
	token := sign()
	response := exchange(token, 201)
	var issued githubExchangeResp
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	principal, err := e.Store.LookupCapability(t.Context(), sha256.Sum256([]byte(issued.Token)))
	if err != nil {
		t.Fatal(err)
	}
	c := principal.Capability
	if c.Parent != nil || c.Team != "a" || c.Subject.Kind != capability.SubjectMachine || c.Binding.Kind != capability.BindingPush || c.Binding.Push == nil || c.Binding.Push.RepositoryKey != "gh:42" || c.Binding.Push.Ref != "refs/heads/main" || c.Binding.Push.SHA != strings.Repeat("a", 40) || c.Binding.Push.WorkflowRunID != "1000" || c.RootMetered || c.ExpiresAt == nil {
		t.Fatalf("Actions root lost verified scope: %+v", c)
	}
	claims["repository_owner_id"] = "8"
	exchange(sign(), 403)
	parts := strings.Split(token, ".")
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	signature[0] ^= 1
	parts[2] = base64.RawURLEncoding.EncodeToString(signature)
	exchange(strings.Join(parts, "."), 401)
	engineSQL(t, e.Store, `UPDATE github_runner_bindings SET repository_owner_id=8 WHERE team='a' AND repository_id=42`)
	exchange(token, 403)
	_, err = e.Store.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionActions, Team: c.Team, RegistrationID: "github:42:7", Subject: c.Subject, Grants: c.Grants, Binding: c.Binding, Repositories: c.Repositories, ExpiresAt: c.ExpiresAt})
	var refused *apierr.Error
	if !errors.As(err, &refused) || refused.Code != apierr.Forbidden {
		t.Fatalf("transaction accepted stale binding facts: %v", err)
	}
	var count int
	if err := e.Store.DB().QueryRow(`SELECT COUNT(*) FROM capabilities`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("issuance count=%d %v", count, err)
	}
}
