package acceptance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func loginCapability(t *testing.T, f sessionFlow) capability.Issued {
	t.Helper()
	f.store.SetProvisionPolicy(actionsProvisionPolicy(func(capability.ProvisionSpec) error { return nil }))
	digest := sha256.Sum256([]byte(f.raw))
	origin := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := f.store.ProvisionCapability(t.Context(), capability.ProvisionSpec{
		Kind: capability.ProvisionMember, Authority: &origin, Team: "team",
		Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"},
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func loginRequest(raw, csrf, next string) *http.Request {
	form := url.Values{"token": {raw}, "csrf_token": {csrf}, "next": {next}}
	r := httptest.NewRequest("POST", "https://controller.test/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://controller.test")
	r.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
	return r
}

func TestLoginHTTPAccountCapabilitySession(t *testing.T) {
	f := newSessionFlow(t)
	root := loginCapability(t, f)
	h := f.server.WithDashboard(controller.Dashboard{}).Handler()
	r := loginRequest(root.Raw, "browser-proof", "/runs?run=selected")
	r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: f.raw})
	started := time.Now()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var raw string
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "__Host-sw_session" {
			raw = cookie.Value
			if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge <= 0 {
				t.Fatalf("session cookie: %+v", cookie)
			}
		}
	}
	if w.Code != 303 || raw == "" || raw == f.raw || w.Header().Get("Location") != "/runs?run=selected" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HTTP %d %s headers=%v", w.Code, w.Body.String(), w.Header())
	}
	if w.Header().Get("X-Request-Id") == "" || !strings.Contains(f.logs.String(), "route=/login") || !strings.Contains(f.logs.String(), "principal_id=member") {
		t.Fatalf("sign-in audit: %s", f.logs.String())
	}
	session, err := f.store.LookupSession(t.Context(), raw)
	if err != nil || session.AccountID != "member" || session.CapabilityPrefix != root.Capability.Prefix || session.Team != "team" {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	if session.ExpiresAt.Before(started.Add(7*24*time.Hour-time.Second)) || session.ExpiresAt.After(time.Now().Add(7*24*time.Hour+time.Second)) {
		t.Fatalf("session lifetime: %s", session.ExpiresAt)
	}
	digest := sha256.Sum256([]byte(raw))
	var stored, plaintext int
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*), COUNT(CASE WHEN hash=? THEN 1 END) FROM sessions WHERE hash IN (?,?)`, raw, raw, hex.EncodeToString(digest[:])).Scan(&stored, &plaintext); err != nil || stored != 1 || plaintext != 0 {
		t.Fatalf("session digest: stored=%d plaintext=%d err=%v", stored, plaintext, err)
	}
	columns, err := f.store.DB().QueryContext(t.Context(), `SELECT * FROM sessions LIMIT 0`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := columns.Columns()
	columns.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "csrf_token" {
			t.Fatal("plaintext CSRF column survives")
		}
	}
	if _, err := f.store.LookupSession(t.Context(), f.raw); err == nil {
		t.Fatal("replaced session survived")
	}
	r = httptest.NewRequest("GET", "https://controller.test/api/v1/auth/whoami", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: raw})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var identity struct{ Scopes []capability.Grant }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &identity) != nil || len(identity.Scopes) != 1 || identity.Scopes[0] != capability.RunsRead {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	tenant, err := f.store.ForTeam(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}
	if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: ref, CapabilityPrefix: ref.ID}, ref); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assertEngineError(t, w, 401, apierr.Unauthenticated)
	if strings.Contains(f.logs.String(), root.Raw) || strings.Contains(f.logs.String(), raw) || strings.Contains(f.logs.String(), session.CSRFToken) {
		t.Fatal("credential entered audit")
	}
}

func TestLoginHTTPProofProperty(t *testing.T) {
	f := newSessionFlow(t)
	root := loginCapability(t, f)
	h := f.server.WithDashboard(controller.Dashboard{}).Handler()
	property := func(value []byte, mutation uint8) bool {
		forged := "forged-" + hex.EncodeToString(value)
		r := loginRequest(root.Raw, "browser-proof", "/")
		switch mutation % 7 {
		case 0:
			r.Header.Set("Origin", "https://foreign.test")
		case 1:
			r.Header.Del("Origin")
		case 2:
			r.Header.Del("Cookie")
		case 3:
			r.Header.Set("Cookie", "__Host-sw_csrf="+forged)
		case 4:
			r.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: "browser-proof"})
		case 5:
			r.Header.Set("Authorization", "Bearer "+root.Raw)
		case 6:
			r.Body = http.NoBody
		}
		before, expiry := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		after, renewed := f.state(t)
		var result apierr.Envelope
		return (w.Code == 400 || w.Code == 401 || w.Code == 403 || w.Code == 429) && before == after && expiry == renewed && json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Error != nil && len(w.Result().Cookies()) == 0 && !strings.Contains(w.Body.String(), root.Raw)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 28}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginHTTPRefusesDataScopeProperty(t *testing.T) {
	f := newSessionFlow(t)
	root := loginCapability(t, f)
	b := newCredentialBoundary()
	b.raw = root.Raw
	b.snapshot.Now = time.Now()
	p := capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Capability.Prefix}, Capability: root.Capability}
	b.snapshot.Authorities = []capability.AuthorityState{{Principal: p, MembershipValid: true}}
	h := f.server.WithAuthorization(consoleAuthorization(b)).WithDashboard(controller.Dashboard{}).Handler()
	property := func(value []byte, publish bool) bool {
		scope := &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "repo-" + hex.EncodeToString(value), ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
		if publish {
			scope.Use, scope.RunID = capability.DataPublish, "run"
		}
		b.snapshot.Authorities[0].Principal.Capability.Data = scope
		before, expiry := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, loginRequest(root.Raw, "browser-proof", "/"))
		after, renewed := f.state(t)
		var result apierr.Envelope
		ok := w.Code == 403 && json.Unmarshal(w.Body.Bytes(), &result) == nil && result.Error != nil && result.Error.Code == apierr.Forbidden && before == after && expiry == renewed && len(w.Result().Cookies()) == 0
		if !ok {
			t.Logf("scoped sign-in: status=%d body=%s sessions=%d->%d", w.Code, w.Body.String(), before, after)
		}
		return ok
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginHTTPPageAndMethods(t *testing.T) {
	f := newSessionFlow(t)
	h := f.server.WithDashboard(controller.Dashboard{Version: "</script><script>alert(1)</script>\u2028"}).Handler()
	for _, state := range []string{"anonymous", "signed-in", "stale"} {
		r := httptest.NewRequest("GET", "https://controller.test/login?next=%2Fruns", nil)
		if state == "signed-in" {
			r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: f.raw})
		} else if state == "stale" {
			r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: "gone"})
		}
		before, expiry := f.state(t)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		after, renewed := f.state(t)
		if before != after || expiry != renewed {
			t.Fatal("login page renewed a session")
		}
		if state == "signed-in" {
			if w.Code != 303 || w.Header().Get("Location") != "/runs" {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			continue
		}
		var csrf string
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == "__Host-sw_csrf" && cookie.MaxAge > 0 {
				csrf = cookie.Value
			}
		}
		if w.Code != 200 || csrf == "" || !strings.Contains(w.Body.String(), `name="csrf_token" value="`+csrf+`"`) || !strings.Contains(w.Body.String(), "Account access token") {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
	}
	for _, method := range []string{"HEAD", "PUT", "DELETE"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "https://controller.test/login", nil))
		assertEngineError(t, w, 405, apierr.MethodNotAllowed)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://controller.test/sparkwing-runtime.js", nil))
	if w.Code != 200 || strings.ContainsAny(w.Body.String(), "\u2028\u2029") || strings.Contains(w.Body.String(), "</script>") || strings.Contains(w.Body.String(), f.raw) {
		t.Fatalf("runtime script: HTTP %d %s", w.Code, w.Body.String())
	}
}

func TestLoginHTTPRefusesInvalidCredentialAndForm(t *testing.T) {
	for _, state := range []string{"unknown", "machine", "operator", "session presentation", "membership", "duplicate token", "unknown field", "large body", "backend"} {
		t.Run(state, func(t *testing.T) {
			f := newSessionFlow(t)
			root := loginCapability(t, f)
			r := loginRequest(root.Raw, "browser-proof", "/")
			code := apierr.Unauthenticated
			switch state {
			case "unknown":
				r = loginRequest("swk_"+strings.Repeat("a", 43), "browser-proof", "/")
			case "machine", "operator":
				spec := capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "operator"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens}}
				if state == "machine" {
					digest := sha256.Sum256([]byte(f.raw))
					origin := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
					spec = capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &origin, Team: "team", Repositories: []string{"github.com/acme/*"}, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "runner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim}, RegistrationID: "runner:team:runner"}
				}
				issued, err := f.store.ProvisionCapability(t.Context(), spec)
				if err != nil {
					t.Fatal(err)
				}
				r, code = loginRequest(issued.Raw, "browser-proof", "/"), apierr.Forbidden
			case "session presentation":
				r = loginRequest(f.raw, "browser-proof", "/")
			case "membership":
				if _, err := f.store.DB().ExecContext(t.Context(), `DELETE FROM memberships`); err != nil {
					t.Fatal(err)
				}
			case "duplicate token", "unknown field":
				body := "token=" + url.QueryEscape(root.Raw) + "&csrf_token=browser-proof&"
				if state == "duplicate token" {
					body += "token=other"
				} else {
					body += "team=foreign"
				}
				r.Body, code = io.NopCloser(strings.NewReader(body)), apierr.BadRequest
			case "large body":
				r.Body, code = io.NopCloser(strings.NewReader("token="+strings.Repeat("x", 65536))), apierr.BadRequest
			case "backend":
				if _, err := f.store.DB().ExecContext(t.Context(), `DROP TABLE capabilities`); err != nil {
					t.Fatal(err)
				}
				code = apierr.Unavailable
			}
			before, expiry := f.state(t)
			w := httptest.NewRecorder()
			f.server.WithDashboard(controller.Dashboard{}).Handler().ServeHTTP(w, r)
			after, renewed := f.state(t)
			assertEngineError(t, w, apierr.Status(code), code)
			if before != after || expiry != renewed || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), root.Raw) || strings.Contains(f.logs.String(), root.Raw) {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestLoginHTTPSafeReturnPath(t *testing.T) {
	for next, want := range map[string]string{"/runs?run=r1": "/runs?run=r1", "https://foreign.test": "/", "//foreign.test/x": "/", `/\foreign.test`: "/", "/runs#fragment": "/", "runs": "/"} {
		t.Run(next, func(t *testing.T) {
			f := newSessionFlow(t)
			root := loginCapability(t, f)
			w := httptest.NewRecorder()
			f.server.WithDashboard(controller.Dashboard{}).Handler().ServeHTTP(w, loginRequest(root.Raw, "browser-proof", next))
			if w.Code != 303 || w.Header().Get("Location") != want {
				t.Fatalf("HTTP %d location=%s", w.Code, w.Header().Get("Location"))
			}
		})
	}
}

func TestLoginHTTPNonlocalRequiresSession(t *testing.T) {
	h := controller.New(nil, nil).WithDashboard(controller.Dashboard{}).Handler()
	for _, path := range []string{"/", "/runs", "/operator"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "https://controller.test"+path, nil))
		if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") {
			t.Fatalf("page %s HTTP %d %s", path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://controller.test/login?next=%2Fruns", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Account access token") {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/login/bootstrap", "/api/v1/auth/login"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "https://controller.test"+path, nil))
		if w.Code != 404 && w.Code != 405 {
			t.Fatalf("POST %s: HTTP %d %s", path, w.Code, w.Body.String())
		}
	}
}
