package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func FuzzRunnerEnrollmentNeverReplays(f *testing.F) {
	f.Add("desk", "prod", uint8(0))
	f.Add("worker", "release", uint8(1))
	f.Add("runner", "build", uint8(2))
	f.Fuzz(func(t *testing.T, name, pool string, failure uint8) {
		name, pool = "runner-"+hex.EncodeToString([]byte(name)), "pool-"+hex.EncodeToString([]byte(pool))
		calls := 0
		manager, err := NewRunnerTokenManager(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.Method != "POST" || req.URL.Path != "/api/v1/runners" || req.GetBody != nil || req.Header.Get("Idempotency-Key") != "" {
				t.Fatalf("enrollment can replay or select another route: %s %s", req.Method, req.URL)
			}
			var proposal EnrollRunnerRequest
			if err := json.NewDecoder(req.Body).Decode(&proposal); err != nil {
				t.Fatal(err)
			}
			if proposal.Name != name || !reflect.DeepEqual(proposal.ReservedPools, []string{pool}) {
				t.Fatalf("enrollment proposal changed: %+v", proposal)
			}
			switch failure % 3 {
			case 0:
				return nil, io.EOF
			case 1:
				return engineResponse(503, `{"error":{"code":"unavailable","message":"reply lost"}}`), nil
			default:
				return engineResponse(201, `{"capability":"one-time-secret","prefix":"prefix","team":"team","grants":[]}`), nil
			}
		})}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := manager.EnrollRunner(t.Context(), EnrollRunnerRequest{Name: name, Repos: []string{"github.com/acme/*"}, ReservedPools: []string{pool}})
		if err == nil || result != nil || calls != 1 {
			t.Fatalf("enrollment calls=%d returned=%v error=%v", calls, result != nil, err)
		}
	})
}

func TestRunnerEnrollmentRefusesDeploymentNamespace(t *testing.T) {
	manager, err := NewTokenManager(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("deployment manager attempted runner enrollment")
		return nil, io.EOF
	})}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.(RunnerTokenManager).EnrollRunner(t.Context(), EnrollRunnerRequest{Name: "desk"}); err == nil {
		t.Fatal("deployment namespace accepted runner enrollment")
	}
}

func TestCredentialManagementUsesCurrentControllerContract(t *testing.T) {
	for _, deployment := range []bool{false, true} {
		base, kind, subject := "/api/v1/tokens", "member", capability.SubjectAccount
		if deployment {
			base, kind, subject = "/api/v1/operator/tokens", "service", capability.SubjectService
		}
		metadata := capability.Metadata{Prefix: "swk_prefix", Subject: capability.Subject{Kind: subject, ID: "build"}, Audience: capability.Controller, Grants: []capability.Grant{capability.RunsRead}, IssuedAt: time.Unix(1, 0).UTC()}
		calls := 0
		manager, err := NewTokenManager(EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "credential", nil }, HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.Header.Get("Authorization") != "Bearer credential" || !strings.HasPrefix(req.URL.Path, base) {
				t.Fatalf("wrong namespace: %s %v", req.URL, req.Header)
			}
			var result any
			switch calls {
			case 1:
				if req.Method != "GET" || req.URL.Path != base || req.URL.Query().Get("kind") != string(subject) || req.URL.Query().Get("include_revoked") != "1" {
					t.Fatalf("list: %s %s", req.Method, req.URL)
				}
				result = struct {
					Tokens []capability.Metadata `json:"tokens"`
				}{[]capability.Metadata{metadata}}
			case 2:
				if req.Method != "GET" || req.URL.Path != base+"/swk_prefix" {
					t.Fatalf("lookup: %s %s", req.Method, req.URL)
				}
				result = metadata
			case 3:
				if req.Method != "POST" || req.URL.Path != base || req.Header.Get("Idempotency-Key") != "" || req.GetBody != nil {
					t.Fatalf("creation may replay: %s %s %v", req.Method, req.URL, req.Header)
				}
				var input map[string]json.RawMessage
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				wantFields := 1
				if deployment {
					wantFields = 2
					if string(input["name"]) != `"build"` {
						t.Fatalf("service name: %s", input["name"])
					}
				}
				if len(input) != wantFields || string(input["grants"]) != `["runs.read"]` {
					t.Fatalf("create input: %s", input)
				}
				result = CreatedToken{Capability: "swk_new-secret", Prefix: metadata.Prefix, Team: metadata.Team, Grants: []string{"runs.read"}}
			case 4:
				if req.Method != "POST" || req.URL.Path != base+"/swk_prefix/rotate" || req.Header.Get("Idempotency-Key") != "" || req.GetBody != nil {
					t.Fatalf("rotation may replay: %s %s %v", req.Method, req.URL, req.Header)
				}
				var input map[string]json.RawMessage
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if len(input) != 1 || string(input["grace_secs"]) != "60" {
					t.Fatalf("rotate: %s", input)
				}
				result = map[string]any{"capability": "swk_rotated-secret", "new": metadata, "old_prefix": "swk_prefix", "old_revoked_at": time.Unix(42, 0).UTC()}
			case 5:
				if req.Method != "DELETE" || req.URL.Path != base+"/swk_prefix" {
					t.Fatalf("revoke: %s %s", req.Method, req.URL)
				}
				return engineResponse(http.StatusNoContent, ""), nil
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
		if err != nil || len(list.Tokens) != 1 || list.Tokens[0].Principal != "build" || list.Tokens[0].Subject == nil || list.Tokens[0].Subject.Kind != subject || list.Tokens[0].IssuedAt == nil || *list.Tokens[0].IssuedAt != 1 {
			t.Fatalf("list: %+v %v", list, err)
		}
		if _, err := manager.ReadToken(t.Context(), "swk_prefix"); err != nil {
			t.Fatal(err)
		}
		zero := int64(0)
		created, err := manager.CreateToken(t.Context(), controllerwire.TokenCreate{Kind: kind, Principal: "build", Scopes: metadata.Grants, TTLSecs: &zero, IdempotencyKey: "unused"})
		if err != nil || created.Token != "swk_new-secret" || created.Metadata.Prefix != metadata.Prefix || !reflect.DeepEqual(created.Metadata.Scopes, metadata.Grants) {
			t.Fatalf("create: %+v %v", created, err)
		}
		rotated, err := manager.RotateToken(t.Context(), "swk_prefix", controllerwire.TokenRotate{GraceSecs: 60, IdempotencyKey: "unused"})
		if err != nil || rotated.OldRevokedAt != 42 || rotated.Metadata.Subject == nil || rotated.Metadata.Subject.ID != "build" {
			t.Fatalf("rotate: %+v %v", rotated, err)
		}
		if _, err := manager.RevokeToken(t.Context(), "swk_prefix"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCredentialIssuanceDoesNotReplayLostBearer(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		calls := 0
		manager, err := NewTokenManager(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.GetBody != nil || req.Header.Get("Idempotency-Key") != "" {
				t.Fatal("one-time issuance configured for replay")
			}
			return nil, io.EOF
		})}}, false)
		if err != nil {
			t.Fatal(err)
		}
		if rotate {
			_, err = manager.RotateToken(t.Context(), "prefix", controllerwire.TokenRotate{IdempotencyKey: "key"})
		} else {
			_, err = manager.CreateToken(t.Context(), controllerwire.TokenCreate{Kind: "member", IdempotencyKey: "key"})
		}
		if err == nil || calls != 1 {
			t.Fatalf("calls=%d error=%v", calls, err)
		}
	}
}

func TestCredentialResponsesRejectMissingOrConflictingIdentity(t *testing.T) {
	for _, body := range []string{`{}`, `{"tokens":[{}]}`, `{"tokens":[{"prefix":"p","subject":{"kind":"service","id":"build"},"audience":"controller","grants":[],"issued_at":"2026-01-01T00:00:00Z"}]}`, `{"tokens":[{"prefix":"p","subject":{"kind":"account","id":"build"},"audience":"controller","grants":[],"issued_at":"2026-01-01T00:00:00Z"},{"prefix":"p","subject":{"kind":"account","id":"build"},"audience":"controller","grants":[],"issued_at":"2026-01-01T00:00:00Z"}]}`} {
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
		manager, err := NewTokenManager(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid request reached controller")
			return nil, errors.New("unexpected")
		})}}, deployment)
		if err != nil {
			t.Fatal(err)
		}
		kind := "service"
		if deployment {
			kind = "member"
		}
		if _, err := manager.CreateToken(t.Context(), controllerwire.TokenCreate{Kind: kind}); err == nil {
			t.Fatal("cross-namespace type accepted")
		}
		if _, err := manager.RotateToken(t.Context(), "prefix", controllerwire.TokenRotate{GraceSecs: -1}); err == nil {
			t.Fatal("negative grace accepted")
		}
		if _, err := manager.RevokeToken(t.Context(), "prefix\r\nsecret"); err == nil {
			t.Fatal("invalid selector accepted")
		}
	}
}

func TestCredentialTTLBecomesAbsoluteExpiry(t *testing.T) {
	ttl := int64(3600)
	before := time.Now().UTC()
	got, err := tokenExpiry(&ttl)
	after := time.Now().UTC()
	if err != nil || got == nil || got.Before(before.Add(time.Hour)) || got.After(after.Add(time.Hour)) {
		t.Fatalf("expiry=%v error=%v", got, err)
	}
}

func FuzzCredentialInventoryPreservesNamespace(f *testing.F) {
	f.Add("account", "member", false)
	f.Add("machine", "runner", false)
	f.Add("service", "launcher", true)
	f.Add("operator", "admin", true)
	f.Fuzz(func(t *testing.T, kind, principal string, deployment bool) {
		metadata := capability.Metadata{Prefix: "prefix", Subject: capability.Subject{Kind: capability.SubjectKind(kind), ID: principal}, Audience: capability.Controller, Grants: []capability.Grant{}, IssuedAt: time.Unix(1, 0).UTC()}
		body, err := json.Marshal(struct {
			Tokens []capability.Metadata `json:"tokens"`
		}{[]capability.Metadata{metadata}})
		if err != nil {
			t.Fatal(err)
		}
		manager, err := NewTokenManager(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
			return engineResponse(200, string(body)), nil
		})}}, deployment)
		if err != nil {
			t.Fatal(err)
		}
		got, err := manager.ListTokens(t.Context(), controllerwire.TokenQuery{})
		if err != nil {
			return
		}
		allowed := kind == "account" || kind == "machine"
		if deployment {
			allowed = kind == "service" || kind == "operator"
		}
		if !allowed || len(got.Tokens) != 1 || got.Tokens[0].Subject == nil || string(got.Tokens[0].Subject.Kind) != kind {
			t.Fatal("credential inventory crossed the selected authority namespace")
		}
	})
}
