package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestCredentialManagementSeparatesTeamAndDeploymentRoutes(t *testing.T) {
	for _, deployment := range []bool{false, true} {
		base, kind := "/api/v1/tokens", "member"
		if deployment {
			base, kind = "/api/v1/operator/tokens", "service"
		}
		metadata := controllerwire.TokenMetadata{Prefix: "swk_prefix", Kind: kind, Principal: "build", Scopes: []capability.Grant{capability.RunsRead}}
		calls := 0
		manager, err := NewTokenManager(EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "credential", nil }, HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.Header.Get("Authorization") != "Bearer credential" || !strings.HasPrefix(req.URL.Path, base) {
				t.Fatalf("wrong namespace: %s %v", req.URL, req.Header)
			}
			var result any
			switch calls {
			case 1:
				if req.Method != "GET" || req.URL.Path != base || req.URL.Query().Get("kind") != kind || req.URL.Query().Get("include_revoked") != "true" {
					t.Fatalf("list: %s %s", req.Method, req.URL)
				}
				result = controllerwire.Tokens{Tokens: []controllerwire.TokenMetadata{metadata}}
			case 2:
				if req.Method != "GET" || req.URL.Path != base+"/swk_prefix" {
					t.Fatalf("lookup: %s %s", req.Method, req.URL)
				}
				result = metadata
			case 3:
				if req.Method != "POST" || req.URL.Path != base || req.Header.Get("Idempotency-Key") != "create-key" {
					t.Fatalf("create: %s %s %v", req.Method, req.URL, req.Header)
				}
				var input controllerwire.TokenCreate
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if input.Kind != kind || input.TTLSecs == nil || *input.TTLSecs != 0 || !reflect.DeepEqual(input.Scopes, metadata.Scopes) {
					t.Fatalf("create input: %+v", input)
				}
				result = controllerwire.TokenCreated{Token: "swk_new-secret", Metadata: metadata}
			case 4:
				if req.Method != "POST" || req.URL.Path != base+"/swk_prefix/rotate" || req.Header.Get("Idempotency-Key") != "rotate-key" {
					t.Fatalf("rotate: %s %s", req.Method, req.URL)
				}
				result = controllerwire.TokenRotated{Token: "swk_rotated-secret", Metadata: metadata, OldPrefix: "swk_prefix", OldRevokedAt: 42}
			case 5:
				if req.Method != "DELETE" || req.URL.Path != base+"/swk_prefix" {
					t.Fatalf("revoke: %s %s", req.Method, req.URL)
				}
				result = controllerwire.TokenRevoked{Prefix: "swk_prefix", Revoked: true}
			default:
				t.Fatal("unexpected request")
			}
			body, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			return engineResponse(200, string(body)), nil
		})}}, deployment)
		if err != nil {
			t.Fatal(err)
		}
		list, err := manager.ListTokens(t.Context(), controllerwire.TokenQuery{Kind: kind, IncludeRevoked: true})
		if err != nil || len(list.Tokens) != 1 {
			t.Fatalf("list: %+v %v", list, err)
		}
		if _, err := manager.ReadToken(t.Context(), "swk_prefix"); err != nil {
			t.Fatal(err)
		}
		zero := int64(0)
		created, err := manager.CreateToken(t.Context(), controllerwire.TokenCreate{Kind: kind, Principal: "build", Scopes: metadata.Scopes, TTLSecs: &zero, IdempotencyKey: "create-key"})
		if err != nil || created.Token != "swk_new-secret" {
			t.Fatalf("create: %+v %v", created, err)
		}
		rotated, err := manager.RotateToken(t.Context(), "swk_prefix", controllerwire.TokenRotate{GraceSecs: 60, IdempotencyKey: "rotate-key"})
		if err != nil || rotated.OldRevokedAt != 42 {
			t.Fatalf("rotate: %+v %v", rotated, err)
		}
		if _, err := manager.RevokeToken(t.Context(), "swk_prefix"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCredentialCreationRetriesWithExactBodyAndKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var bodies, keys []string
		manager, err := NewTokenManager(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, string(body))
			keys = append(keys, req.Header.Get("Idempotency-Key"))
			if len(bodies) == 1 {
				return nil, io.EOF
			}
			return engineResponse(200, `{"token":"swk_secret","metadata":{"prefix":"swk_prefix","kind":"member","principal":"build","scopes":[]}}`), nil
		})}}, false)
		if err != nil {
			t.Fatal(err)
		}
		_, err = manager.CreateToken(t.Context(), controllerwire.TokenCreate{Kind: "member", Principal: "build", IdempotencyKey: "stable"})
		if err != nil || len(bodies) != 2 || bodies[0] != bodies[1] || !reflect.DeepEqual(keys, []string{"stable", "stable"}) {
			t.Fatalf("retry: %v %v %v", bodies, keys, err)
		}
	})
}

func TestCredentialResponsesRejectMissingOrConflictingIdentity(t *testing.T) {
	for _, body := range []string{`{}`, `{"tokens":[{}]}`, `{"tokens":[{"prefix":"p","kind":"service","principal":"build","scopes":[]}]}`, `{"tokens":[{"prefix":"p","kind":"member","principal":"build","scopes":[]},{"prefix":"p","kind":"member","principal":"build","scopes":[]}]}`} {
		manager, err := NewTokenManager(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })}}, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ListTokens(t.Context(), controllerwire.TokenQuery{}); err == nil {
			t.Fatalf("invalid metadata accepted: %s", body)
		}
	}
}

func TestCredentialManagementNeverCrossesAuthorityNamespace(t *testing.T) {
	for _, deployment := range []bool{false, true} {
		manager, _ := NewTokenManager(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid request reached controller")
			return nil, fmt.Errorf("unexpected")
		})}}, deployment)
		kind := "service"
		if deployment {
			kind = "member"
		}
		if _, err := manager.CreateToken(t.Context(), controllerwire.TokenCreate{Kind: kind, IdempotencyKey: "key"}); err == nil {
			t.Fatal("cross-namespace type accepted")
		}
		if _, err := manager.RotateToken(t.Context(), "prefix", controllerwire.TokenRotate{GraceSecs: -1, IdempotencyKey: "key"}); err == nil {
			t.Fatal("negative grace accepted")
		}
		if _, err := manager.RevokeToken(t.Context(), "prefix\r\nsecret"); err == nil {
			t.Fatal("invalid selector accepted")
		}
	}
}
