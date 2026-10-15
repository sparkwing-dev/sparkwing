package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestServerPolicyUsesCurrentPersistedAuthority(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	s := e.Store
	engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','reader',1)`)
	key := sha256.Sum256([]byte("member-token"))
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(key[:])
	digest := sha256.Sum256([]byte(raw))
	c := capability.Capability{Prefix: "member", Digest: digest, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"},
		Audience: capability.Controller, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone},
		Issuance: capability.Delegate, IssuedAt: time.Unix(0, 1).UTC(), ReservedPools: []string{}, Repositories: []string{}}
	envelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: c})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s, `INSERT INTO capabilities(digest,prefix,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,repo_scope,created_at,created_by) VALUES (?,'member','a','account','member','controller',?,'delegate',1,?,'none',?,?,1,'member')`, hex.EncodeToString(digest[:]), []byte(`["runs.read"]`), envelope, []byte(`[]`), []byte(`[]`))
	h := New(s, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	request := func(token string) int {
		r := httptest.NewRequest("GET", "/api/v1/runs/run/jobs", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := request(raw); got != http.StatusOK {
		t.Fatalf("persisted capability = %d", got)
	}
	engineSQL(t, s, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
	if got := request(raw); got != http.StatusUnauthorized {
		t.Fatalf("removed membership = %d", got)
	}
	engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','reader',1)`)
	if got := request(raw); got != http.StatusOK {
		t.Fatalf("restored membership = %d", got)
	}
	tenant, err := s.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"}
	if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: ref, CapabilityPrefix: ref.ID}, ref); err != nil {
		t.Fatal(err)
	}
	if got := request(raw); got != http.StatusUnauthorized {
		t.Fatalf("revoked capability = %d", got)
	}
}

func TestServerPolicyNeverInfersAuthorityFromRequestHints(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	property := func(input []byte, includeBearer bool) bool {
		hint := base64.RawURLEncoding.EncodeToString(input)
		r := httptest.NewRequest("GET", "/api/v1/runs/run/jobs?team=a&generation=1", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Sparkwing-Principal", hint)
		r.Header.Set("X-Sparkwing-Holder", hint)
		r.Header.Set("X-Sparkwing-Team", "a")
		r.Header.Set("X-Sparkwing-Generation", "1")
		if includeBearer {
			r.Header.Set("Authorization", "Bearer "+hint)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code == http.StatusUnauthorized
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

type sessionDigestFixture struct{ store *store.Store }

func (f sessionDigestFixture) ReadAuthentication(ctx context.Context, lookup capability.CredentialLookup, evidence capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	snapshot, err := f.store.ReadAuthentication(ctx, lookup, evidence)
	if err == nil && lookup.Class == capability.CredentialSession && len(snapshot.Authorities) > 0 && snapshot.Authorities[0].Principal.Authority.ID == hex.EncodeToString(lookup.Digest[:]) {
		snapshot.Authorities[0].Principal.Capability.Digest = lookup.Digest
	}
	return snapshot, err
}

func TestServerPolicySessionsCheckCommittedIdentityAndCSRF(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	s := e.Store
	engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','editor',1)`)
	raw := "private-account-session"
	digest := sha256.Sum256([]byte(raw))
	engineSQL(t, s, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,'member','runs.read,runs.control',1,?,'a','member')`, hex.EncodeToString(digest[:]), time.Now().Add(time.Hour).UnixNano())
	server := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	authorization := server.authorization
	authorization.Authenticator = capability.NewAuthenticator(sessionDigestFixture{s})
	server.WithAuthorization(authorization)
	authorization.CheckCSRF = func(r *http.Request, p capability.Principal) error {
		if p.Authority.ID != hex.EncodeToString(digest[:]) || !validAPIRequestCSRF(r, "committed-session-csrf", server.cookiesSecure()) {
			return &apierr.Error{Code: apierr.Forbidden, Message: "invalid CSRF token"}
		}
		return nil
	}
	h := server.Handler()
	request := func(method, path, sessionID, token, origin string) int {
		r := httptest.NewRequest(method, "https://controller.test"+path, strings.NewReader(`{"reason":"user requested"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set(csrfHeaderName, token)
		r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, server.cookiesSecure()), Value: sessionID})
		r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, server.cookiesSecure()), Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("GET", "/api/v1/runs/run/jobs", raw, "", ""); got != 200 {
		t.Fatalf("account session read = %d", got)
	}
	if got := request("POST", "/api/v1/runs/run/cancel", raw, "committed-session-csrf", "https://controller.test"); got != 500 {
		t.Fatalf("missing CSRF adapter = %d", got)
	}
	server.WithAuthorization(authorization)
	h = server.Handler()
	property := func(value []byte) bool {
		forged := "forged-" + base64.RawURLEncoding.EncodeToString(value)
		return request("POST", "/api/v1/runs/run/cancel", raw, forged, "https://controller.test") == 403 &&
			request("POST", "/api/v1/runs/run/cancel", raw, "committed-session-csrf", "https://foreign.test") == 403
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	if got := request("POST", "/api/v1/runs/run/cancel", raw, "committed-session-csrf", "https://controller.test"); got != 200 {
		t.Fatalf("same-origin session mutation = %d", got)
	}
	engineSQL(t, s, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
	if got := request("GET", "/api/v1/runs/run/jobs", raw, "", ""); got != 401 {
		t.Fatalf("removed session membership = %d", got)
	}
}
