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
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/internal/jwks/jwkstest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
		"workflow_ref": "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main", "run_id": "1000", "run_attempt": "1",
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
	principal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(issued.Token)))
	if err != nil {
		t.Fatal(err)
	}
	c := principal.Capability
	if c.Parent != nil || c.Team != "a" || c.Subject.Kind != capability.SubjectMachine || c.Binding.Kind != capability.BindingPush || c.Binding.Push == nil || c.Binding.Push.RepositoryKey != "gh:42" || c.Binding.Push.Ref != "refs/heads/main" || c.Binding.Push.SHA != strings.Repeat("a", 40) || c.Binding.Push.WorkflowRunID != "1000" || c.RootMetered || c.ExpiresAt == nil {
		t.Fatalf("Actions root lost verified scope: %+v", c)
	}
	exchange(token, 403)
	claims["run_attempt"] = "2"
	exchange(sign(), 201)
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
	_, err = e.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionActions, Team: c.Team, RegistrationID: "github:42:7", Subject: c.Subject, Grants: c.Grants, Binding: c.Binding, Repositories: c.Repositories, ExpiresAt: c.ExpiresAt})
	var refused *apierr.Error
	if !errors.As(err, &refused) || refused.Code != apierr.Forbidden {
		t.Fatalf("transaction accepted stale binding facts: %v", err)
	}
	var count int
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM capabilities`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("issuance count=%d %v", count, err)
	}
}

func TestActionsExpiryFitsStoreClockProperty(t *testing.T) {
	for _, year := range []int{2020, 2030} {
		t.Run(fmt.Sprint(year), func(t *testing.T) {
			databaseNow := time.Date(year, time.January, 1, 12, 0, 0, 123456789, time.UTC)
			e, _ := engineRuntimeStore(t, teststore.WithClock(func() time.Time { return databaseNow }))
			engineSQL(t, e.Store, `INSERT INTO github_runner_bindings(team,repository_id,repository_owner_id,repository,created_by,created_at,repo_key) VALUES ('a',42,7,'acme/repo','member',1,'gh:42')`)
			server := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil)))
			property := func(input uint32) bool {
				defer func() { databaseNow = databaseNow.Add(time.Hour) }()
				// Token validity is checked before issuance, which can wait on the store.
				tokenExpiry := databaseNow.Add(time.Duration(int64(input%7200)-3600) * time.Second)
				spec, err := server.actionsProvision(t.Context(), "a", githuboidc.Claims{RepositoryID: 42, RepositoryOwnerID: 7, Ref: "refs/heads/main", SHA: fmt.Sprintf("%040x", input), RunID: fmt.Sprint(input), RunAttempt: "1", WorkflowRef: "acme/repo/.github/workflows/sparkwing.yaml@refs/heads/main", ExpiresAt: tokenExpiry})
				if err != nil {
					t.Log(err)
					return false
				}
				issued, err := e.Store.AsOperator().ProvisionCapability(t.Context(), spec)
				want := databaseNow.Add(time.Hour)
				if ceiling := tokenExpiry.Add(time.Hour); ceiling.Before(want) {
					want = ceiling
				}
				if !want.After(databaseNow) {
					var refused *apierr.Error
					return errors.As(err, &refused) && refused.Code == apierr.Forbidden
				}
				if err != nil || issued.Capability.ExpiresAt == nil || !issued.Capability.ExpiresAt.Equal(want) || !issued.Capability.IssuedAt.Equal(databaseNow) {
					t.Logf("Actions lifetime: %+v, %v; want %s", issued.Capability, err, want)
					return false
				}
				return true
			}
			for _, input := range []uint32{0, 1, 3599, 3600, 7199} {
				if !property(input) {
					t.Fatalf("Actions expiry differs from its store clock for %d", input)
				}
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
