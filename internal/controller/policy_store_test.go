package controller

import (
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

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestLocalServerAcceptsBearerWithSessionAncestry(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := credentialSessionServerWith(t, func(s *Server) { s.WithSecretsCipher(cipher) })
	raw, _, _, err := s.store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(raw))
	ref := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := s.store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionMember, Authority: &ref, Team: "a",
		Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"},
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := New(s.store, nil).Handler()
	engineClaimHTTP(t, h, issued.Raw, "GET", "/api/v1/runs/run/jobs", "", http.StatusOK)
	r := httptest.NewRequest("GET", "/api/v1/runs/run/jobs", nil)
	r.Header.Set("Authorization", "Session "+raw)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("local session presentation: %d %s", w.Code, w.Body)
	}
	if err := s.store.AsOperator().DeleteSession(raw); err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, issued.Raw, "GET", "/api/v1/runs/run/jobs", "", http.StatusUnauthorized)
}

func TestServerPolicyUsesCurrentPersistedAuthority(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	s := e.Store
	engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','reader',1)`)
	key := sha256.Sum256([]byte("member-token"))
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(key[:])
	digest := sha256.Sum256([]byte(raw))
	c := capability.Capability{
		Prefix: "member", Digest: digest, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"},
		Audience: capability.Controller, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone},
		Issuance: capability.Delegate, IssuedAt: time.Unix(0, 1).UTC(), ReservedPools: []string{}, Repositories: []string{},
	}
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

func TestServerPolicySessionsCheckCommittedIdentityAndCSRF(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) })
	s := e.Store
	engineSQL(t, s, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, s, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','editor',1)`)
	server := e.Server
	raw, csrf, _, err := s.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	other, otherCSRF, _, err := s.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := server.Handler()
	request := func(method, path, sessionID, header, cookie, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://controller.test"+path, strings.NewReader(`{"reason":"user requested"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set(csrfHeaderName, header)
		r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, server.cookiesSecure()), Value: sessionID})
		r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, server.cookiesSecure()), Value: cookie})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/v1/runs/run/jobs", raw, "", "", ""); w.Code != 200 {
		t.Fatalf("account session read = %d %s", w.Code, w.Body.String())
	}
	property := func(value []byte) bool {
		forged := "forged-" + base64.RawURLEncoding.EncodeToString(value)
		for _, tokens := range [][3]string{{forged, forged, "https://controller.test"}, {csrf, csrf, "https://foreign.test"}, {csrf, forged, "https://controller.test"}, {otherCSRF, otherCSRF, "https://controller.test"}, {csrf, csrf, ""}} {
			if w := request("POST", "/api/v1/runs/run/cancel", raw, tokens[0], tokens[1], tokens[2]); w.Code != 403 {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	run, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	before, err := run.ReadRun(t.Context(), "run")
	if err != nil || before.CancelRequestedAt != nil {
		t.Fatalf("CSRF refusal changed run: %+v %v", before, err)
	}
	for _, pair := range [][2]string{{raw, csrf}, {other, otherCSRF}} {
		if w := request("POST", "/api/v1/runs/run/cancel", pair[0], pair[1], pair[1], "https://controller.test"); w.Code != 200 {
			t.Fatalf("session mutation = %d %s", w.Code, w.Body.String())
		}
	}

	connection, err := s.Connection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"missing", "wrong"} {
		var keyCipher BoundCipher
		if key == "wrong" {
			bytes := make([]byte, secrets.KeySize)
			bytes[0] = 1
			wrong, err := secrets.NewCipher(bytes)
			if err != nil {
				t.Fatal(err)
			}
			keyCipher = wrong
		}
		fixture := &serverFixture{}
		reopened, err := teststore.OpenExisting(strings.TrimPrefix(connection.Target, "sqlite:"), fixture.option(server.deployment, func(s *Server) { s.WithSecretsCipher(keyCipher) }))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := reopened.Close(); err != nil {
				t.Error(err)
			}
		})
		h = fixture.Server.Handler()
		if w := request("POST", "/api/v1/runs/run/cancel", raw, csrf, csrf, "https://controller.test"); w.Code != 503 || strings.Contains(w.Body.String(), raw) || strings.Contains(w.Body.String(), csrf) {
			t.Fatalf("%s key response = %d %s", key, w.Code, w.Body.String())
		}
	}
	h = server.Handler()
	engineSQL(t, s, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='member'`)
	if w := request("POST", "/api/v1/runs/run/cancel", raw, csrf, csrf, "https://controller.test"); w.Code != 403 {
		t.Fatalf("demoted session = %d %s", w.Code, w.Body.String())
	}
	engineSQL(t, s, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
	if w := request("GET", "/api/v1/runs/run/jobs", raw, "", "", ""); w.Code != 401 {
		t.Fatalf("removed session = %d %s", w.Code, w.Body.String())
	}
}

func TestServerPolicyConsoleUsesDirectSessionAndConfiguredGate(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) })
	st := engine.Store
	engineSQL(t, st, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('operator','operator@example.test',1,1)`)
	engineSQL(t, st, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','operator','reader',1)`)
	s := engine.Server.WithOperatorAccounts([]string{"operator"})
	raw, csrf, _, err := st.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "operator"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	writer := &consoleWritesFake{}
	s.consoleWrites = writer
	property := func(input []byte, phase uint8) bool {
		s.WithOperatorAccounts([]string{"operator"})
		writer.calls = 0
		r := httptest.NewRequest("POST", "https://controller.test/api/v1/operator/teams/a/grants?actor="+hex.EncodeToString(input), strings.NewReader(`{"amount_cents":10,"reason":"test","key":"once"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
		r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
		r.Header.Set(csrfHeaderName, csrf)
		want := 201
		switch phase % 5 {
		case 1:
			s.WithOperatorAccounts(nil)
			want = 403
		case 2:
			r.Header.Set(csrfHeaderName, "foreign-"+hex.EncodeToString(input))
			want = 403
		case 3:
			r.Header.Set("Authorization", "Bearer "+raw)
			want = 401
		case 4:
			r.Header.Set("Origin", "https://foreign.test")
			want = 403
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != want || (writer.calls == 1) != (phase%5 == 0) {
			t.Logf("phase=%d response=%d %s calls=%d", phase%5, w.Code, w.Body, writer.calls)
			return false
		}
		if phase%5 == 0 {
			a := writer.seen
			digest := sha256.Sum256([]byte(raw))
			return a.Principal.Class == capability.CredentialConsole && a.Principal.Capability.Subject == (capability.Subject{Kind: capability.SubjectAccount, ID: "operator"}) && a.Principal.Authority == (capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}) && a.Target.Team == "a"
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}
