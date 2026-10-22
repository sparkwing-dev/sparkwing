package acceptance_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type sessionFlow struct {
	store  *store.Store
	server *controller.Server
	raw    string
	csrf   string
	logs   *bytes.Buffer
	path   string
	config deployment.Config
}

func newSessionFlow(t *testing.T) sessionFlow {
	t.Helper()
	return newDeploymentSessionFlow(t, deploymenttest.Team(t))
}

func newDeploymentSessionFlow(t *testing.T, cfg deployment.Config) sessionFlow {
	t.Helper()
	f := sessionFlow{logs: &bytes.Buffer{}, path: filepath.Join(t.TempDir(), "sessions.db"), config: cfg}
	s, _, server := credentialMutationServer(t, f.path, f.newServer)
	for _, statement := range []string{
		`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('team','member','editor',1)`,
	} {
		if _, err := teststore.SQL(t, s).ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	raw, csrf, _, err := s.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "team", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.store, f.server, f.raw, f.csrf = s, server, raw, csrf
	return f
}

func (f sessionFlow) newServer(s *store.Store, cipher *secrets.Cipher) *controller.Server {
	server := controller.NewDeployment(s, f.config, slog.New(slog.NewTextHandler(f.logs, nil)))
	if cipher != nil {
		server.WithSecretsCipher(cipher)
	}
	return server
}

func (f *sessionFlow) restartWithCipher(t *testing.T, cipher *secrets.Cipher) {
	t.Helper()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	s, server, err := openControllerFixture(f.path, func(s *store.Store) *controller.Server { return f.newServer(s, cipher) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	f.store, f.server = s, server
}

func (f sessionFlow) request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "https://controller.test"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://controller.test")
	r.Header.Set("X-CSRF-Token", f.csrf)
	r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: f.raw})
	r.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: f.csrf})
	return r
}

func (f sessionFlow) state(t *testing.T) (int64, int64) {
	t.Helper()
	var count, expires int64
	if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT COUNT(*),COALESCE(MAX(expires_at),0) FROM sessions`).Scan(&count, &expires); err != nil {
		t.Fatal(err)
	}
	return count, expires
}

func TestSessionFlowHTTPRenewsWithNamedProof(t *testing.T) {
	for _, header := range []bool{false, true} {
		f := newSessionFlow(t)
		r := f.request("GET", "/api/v1/auth/session?team=foreign", "")
		if header {
			r.Header.Set("Authorization", "Session "+f.raw)
			r.Header.Del("Cookie")
		}
		w := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(w, r)
		var result struct {
			Principal string   `json:"principal"`
			Team      string   `json:"team"`
			Role      string   `json:"role"`
			UserID    string   `json:"user_id"`
			CSRF      string   `json:"csrf_token"`
			Scopes    []string `json:"scopes"`
			Expires   int64    `json:"expires_at"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Principal != "member@example.test" || result.Team != "team" || result.Role != "editor" || result.UserID != "member" || result.CSRF != f.csrf || len(result.Scopes) == 0 || strings.Contains(w.Body.String(), f.raw) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
		count, expires := f.state(t)
		if count != 1 || expires/time.Second.Nanoseconds() != result.Expires || result.Expires < teststore.Now(f.store).Add(6*24*time.Hour).Unix() {
			t.Fatalf("stored=%d/%d result=%+v", count, expires, result)
		}
	}
}

func TestSessionFlowHTTPLogoutUsesVerifiedSession(t *testing.T) {
	for _, header := range []bool{false, true} {
		f := newSessionFlow(t)
		other, _, _, err := f.store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "team", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		r := f.request("POST", "/api/v1/auth/logout?session_id="+other, "")
		if header {
			r.Header.Set("Authorization", "Session "+f.raw)
		}
		w := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(w, r)
		count, _ := f.state(t)
		if w.Code != 204 || w.Body.Len() != 0 || count != 1 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("HTTP %d %s count=%d", w.Code, w.Body.String(), count)
		}
		if _, err := f.store.AsOperator().LookupSession(t.Context(), other); err != nil {
			t.Fatalf("logout ended unrelated session: %v", err)
		}
	}
}

func TestSessionFlowHTTPProofProperty(t *testing.T) {
	f := newSessionFlow(t)
	h := f.server.WithDashboard(controller.Dashboard{}).Handler()
	property := func(bytes []byte, mutation uint8) bool {
		method, path, body := "POST", "/api/v1/auth/logout", ""
		if mutation%12 >= 8 {
			method, path = "GET", "/api/v1/auth/session"
		}
		r := f.request(method, path, body)
		forged := "forged-" + hex.EncodeToString(bytes)
		switch mutation % 12 {
		case 0:
			r.Header.Del("X-CSRF-Token")
		case 1:
			r.Header.Set("X-CSRF-Token", forged)
		case 2:
			r.Header.Set("Origin", "https://foreign.test")
		case 3:
			r.Header.Del("Origin")
		case 4:
			r.Header.Set("Cookie", "__Host-sw_session="+f.raw+"; __Host-sw_csrf="+forged)
			r.Header.Set("X-CSRF-Token", forged)
		case 5:
			r.Header.Set("Authorization", "Session "+forged)
		case 6:
			r.Header.Set("Authorization", "Bearer "+f.raw)
		case 7:
			r.Header.Del("Cookie")
		case 8:
			r.Header.Set("Authorization", "Session "+forged)
		case 9:
			r.Header.Add("Authorization", "Session "+f.raw)
			r.Header.Add("Authorization", "Session "+forged)
		case 10:
			r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: forged})
		case 11:
			r.Header.Set("Authorization", "Session "+f.raw+" extra")
		}
		before, expiry := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		after, renewed := f.state(t)
		var result apierr.Envelope
		return (w.Code == 401 || w.Code == 403) && json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Error != nil && before == after && expiry == renewed && w.Header().Get("Cache-Control") == "no-store" && !strings.Contains(w.Body.String(), f.raw) && !strings.Contains(w.Body.String(), f.csrf)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 120}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionFlowHTTPRenewalCeilingProperty(t *testing.T) {
	f := newSessionFlow(t)
	const maximum = 48 * time.Hour
	h := f.server.WithSessionMaxLifetime(maximum).Handler()
	property := func(age uint8) bool {
		if err := f.store.AsOperator().DeleteSession(f.raw); err != nil {
			t.Fatal(err)
		}
		raw, csrf, session, err := f.store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "member"}, "team", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		f.raw, f.csrf = raw, csrf
		now := session.CreatedAt
		elapsed := (time.Duration(age%96) - 24) * time.Hour
		created, expires := now.Add(-elapsed), now.Add(10*time.Minute)
		if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `UPDATE sessions SET created_at=?,expires_at=?`, created.UnixNano(), expires.UnixNano()); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, f.request("GET", "/api/v1/auth/session", ""))
		count, stored := f.state(t)
		if elapsed < 0 || elapsed >= maximum {
			return w.Code == 401 && count == 1 && stored == expires.UnixNano()
		}
		var result struct {
			Expires int64 `json:"expires_at"`
		}
		return w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Expires == stored/time.Second.Nanoseconds() && stored <= created.Add(maximum).UnixNano() && stored >= expires.UnixNano()
	}
	for age := range uint8(96) {
		if !property(age) {
			t.Fatalf("session renewal failed at age %s", (time.Duration(age)-24)*time.Hour)
		}
	}
}

func TestSessionFlowHTTPParentRevocationEndsRenewalAndLogout(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		f := newSessionFlow(t)
		digest := sha256.Sum256([]byte(f.raw))
		parent, err := f.store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
			Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])},
			Team: "team", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"},
			Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone},
		})
		if err != nil {
			t.Fatal(err)
		}
		raw, session, err := f.store.AsOperator().CreateCapabilitySession(t.Context(), parent.Capability.Prefix, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		f.raw, f.csrf = raw, session.CSRFToken
		tenant, err := f.store.ForTeam(t.Context(), "team")
		if err != nil {
			t.Fatal(err)
		}
		authority := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent.Capability.Prefix}
		if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: authority, CapabilityPrefix: authority.ID}, authority); err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/auth/session"
		if method == "POST" {
			path = "/api/v1/auth/logout"
		}
		before, expiry := f.state(t)
		w := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(w, f.request(method, path, ""))
		assertEngineError(t, w, 401, apierr.Unauthenticated)
		after, renewed := f.state(t)
		if before != after || expiry != renewed {
			t.Fatal("revoked parent allowed session renewal or deletion")
		}
	}
}

func TestSessionFlowHTTPRefusesEndedAndInvalidSessions(t *testing.T) {
	for _, state := range []string{"expired", "revoked", "removed member", "maximum lifetime", "future creation", "license", "missing key", "wrong key", "backend", "head", "body"} {
		t.Run(state, func(t *testing.T) {
			cfg := deploymenttest.Team(t)
			if state == "license" {
				cfg = deployment.Config{}
			}
			f := newDeploymentSessionFlow(t, cfg)
			method, path, body, code := "GET", "/api/v1/auth/session", "", apierr.Unauthenticated
			digest := sha256.Sum256([]byte(f.raw))
			query, value := "", int64(0)
			switch state {
			case "expired":
				query, value = "UPDATE sessions SET expires_at=? WHERE hash=?", teststore.Now(f.store).Add(-time.Hour).UnixNano()
			case "revoked":
				query, value = "UPDATE sessions SET revoked_at=? WHERE hash=?", teststore.Now(f.store).UnixNano()
			case "removed member":
				query = "DELETE FROM memberships"
			case "maximum lifetime":
				query, value = "UPDATE sessions SET created_at=? WHERE hash=?", teststore.Now(f.store).Add(-31*24*time.Hour).UnixNano()
			case "future creation":
				query, value = "UPDATE sessions SET created_at=? WHERE hash=?", teststore.Now(f.store).Add(time.Hour).UnixNano()
			case "license":
			case "missing key":
				f.restartWithCipher(t, nil)
				code = apierr.Unavailable
			case "wrong key":
				cipher, err := secrets.NewCipher(bytes.Repeat([]byte{79}, secrets.KeySize))
				if err != nil {
					t.Fatal(err)
				}
				f.restartWithCipher(t, cipher)
				code = apierr.Unavailable
			case "backend":
				query = "DROP TABLE sessions"
				code = apierr.Unavailable
			case "head":
				method, code = "HEAD", apierr.MethodNotAllowed
			case "body":
				method, path, body, code = "POST", "/api/v1/auth/logout", `{"session_id":"foreign"}`, apierr.BadRequest
			}
			if query != "" {
				args := []any{value, hex.EncodeToString(digest[:])}
				if state == "removed member" || state == "backend" {
					args = nil
				}
				if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			r := f.request(method, path, body)
			r.Header.Set("Authorization", "Session "+f.raw)
			w := httptest.NewRecorder()
			f.server.Handler().ServeHTTP(w, r)
			assertEngineError(t, w, apierr.Status(code), code)
			if strings.Contains(w.Body.String(), f.raw) || strings.Contains(w.Body.String(), f.csrf) || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSessionFlowHTTPRolelessAccountHasNoTeamAuthority(t *testing.T) {
	f := newSessionFlow(t)
	for _, statement := range []string{
		`INSERT INTO accounts(id,email,email_verified,created_at,updated_at,waitlist_reason) VALUES ('waitlisted','waitlisted@example.test',1,1,1,'signup_gate')`,
		`INSERT INTO identities(provider,subject,account_id,email_verified,created_at) VALUES ('github','waitlisted','waitlisted',1,1)`,
	} {
		if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	raw, csrf, _, err := f.store.AsOperator().CreateIdentityAccountSession(t.Context(), store.Account{ID: "waitlisted"}, "", "github", "waitlisted", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.raw, f.csrf = raw, csrf
	handler := f.server.Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, f.request("GET", "/api/v1/auth/session", ""))
	var result struct {
		Team   string   `json:"team"`
		Role   string   `json:"role"`
		UserID string   `json:"user_id"`
		Scopes []string `json:"scopes"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Team != "" || result.Role != "" || len(result.Scopes) != 0 {
		t.Fatalf("roleless session: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request("POST", "/api/v1/secrets/rotate", ""))
	if w.Code != http.StatusForbidden {
		t.Fatalf("roleless team write: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request("POST", "/api/v1/auth/logout", ""))
	if w.Code != http.StatusNoContent {
		t.Fatalf("roleless logout: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.store.AsOperator().LookupSession(t.Context(), raw); err == nil {
		t.Fatal("roleless logout kept session live")
	}
}
