package acceptance_test

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

const (
	oauthOrigin   = "https://controller.test"
	oauthRedirect = oauthOrigin + "/auth/github/callback"
)

type oauthProvider struct {
	challenge, code string
	calls           int
	spent           bool
	failure         string
	diagnostic      string
	transportError  error
	userBody        string
	emailsBody      string
	onToken         func()
}

func (p *oauthProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	p.calls++
	if p.transportError != nil {
		return nil, p.transportError
	}
	body, status := "", http.StatusOK
	switch r.URL.Path {
	case "/token":
		if p.onToken != nil {
			p.onToken()
		}
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		if p.failure == "unavailable" {
			return nil, errors.New(p.diagnostic)
		}
		if p.spent || p.failure == "rejected" || r.Form.Get("code") != p.code || googleauth.CodeChallenge(r.Form.Get("code_verifier")) != p.challenge || r.Form.Get("redirect_uri") != oauthRedirect {
			data, err := json.Marshal(map[string]string{"error": p.diagnostic})
			if err != nil {
				return nil, err
			}
			body, status = string(data), http.StatusUnauthorized
		} else {
			p.spent = true
			body = `{"access_token":"private-provider-access-token"}`
		}
	case "/user":
		if r.Header.Get("Authorization") != "Bearer private-provider-access-token" {
			return nil, errors.New("missing provider bearer")
		}
		body = `{"id":123,"login":"member","name":"Member"}`
		if p.userBody != "" {
			body = p.userBody
		}
	case "/emails":
		body = `[{"email":"member@example.test","primary":true,"verified":true}]`
		if p.failure == "unverified" {
			body = `[{"email":"member@example.test","primary":true,"verified":false}]`
		}
		if p.emailsBody != "" {
			body = p.emailsBody
		}
	default:
		return nil, errors.New("unexpected provider request")
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

type oauthHTTP struct {
	sessionFlow
	provider   *oauthProvider
	handler    http.Handler
	flow       *http.Cookie
	oauthState string
	requests   int
}

func newOAuthHTTP(t *testing.T) oauthHTTP {
	t.Helper()
	f := newSessionFlow(t)
	for _, statement := range []string{
		`UPDATE accounts SET active_team='team' WHERE id='member'`,
		`INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','123','member','member@example.test',1,1)`,
	} {
		if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	p := &oauthProvider{code: "private-provider-code", diagnostic: "private-provider-diagnostic"}
	h := withOAuthGitHub(f.server, p).Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", oauthOrigin+"/auth/github/start?next=%2Fruns%3Frun%3Dr1", nil))
	authorize, err := url.Parse(w.Header().Get("Location"))
	flow := oauthCookie(w, "__Host-sw_oauth")
	if w.Code != 303 || err != nil || flow == nil {
		t.Fatalf("start HTTP %d %s", w.Code, w.Body.String())
	}
	p.challenge = authorize.Query().Get("code_challenge")
	if authorize.Query().Get("scope") != "read:user user:email" || p.challenge == "" || authorize.Query().Get("code_challenge_method") != "S256" || authorize.Query().Get("redirect_uri") != oauthRedirect || p.calls != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authorize=%s calls=%d", authorize, p.calls)
	}
	if !flow.HttpOnly || !flow.Secure || flow.SameSite != http.SameSiteLaxMode || flow.Path != "/" || flow.Domain != "" {
		t.Fatalf("flow cookie=%+v", flow)
	}
	return oauthHTTP{sessionFlow: f, provider: p, handler: h, flow: flow, oauthState: authorize.Query().Get("state")}
}

func withOAuthGitHub(s *controller.Server, p *oauthProvider) *controller.Server {
	return s.WithDashboard(controller.Dashboard{}).WithGitHubSignIn(githubauth.New(githubauth.Config{
		ClientID: "client", ClientSecret: "private-client-secret", AuthURL: "https://provider.test/authorize",
		TokenURL: "https://provider.test/token", UserURL: "https://provider.test/user", EmailsURL: "https://provider.test/emails",
		HTTP: &http.Client{Transport: p},
	}), []string{oauthRedirect})
}

func oauthCookie(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func assertRefusedFlow(t *testing.T, w *httptest.ResponseRecorder, location string) {
	t.Helper()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != location || w.Header().Get("Cache-Control") != "no-store" ||
		oauthCookie(w, "__Host-sw_session") != nil || strings.Contains(w.Body.String(), "private-") {
		t.Fatalf("HTTP %d to %q %s, want 303 to %q", w.Code, w.Header().Get("Location"), w.Body.String(), location)
	}
}

func (f *oauthHTTP) callback(query url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", oauthOrigin+"/auth/github/callback?"+query.Encode(), nil)
	f.requests++
	r.RemoteAddr = "192.0.2." + strconv.Itoa(f.requests%254+1) + ":1234"
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func TestOAuthHTTPSessionAndReplay(t *testing.T) {
	f := newOAuthHTTP(t)
	query := url.Values{"state": {f.oauthState}, "code": {f.provider.code}}
	w := f.callback(query, f.flow, &http.Cookie{Name: "__Host-sw_session", Value: f.raw})
	session, csrf, flow := oauthCookie(w, "__Host-sw_session"), oauthCookie(w, "__Host-sw_csrf"), oauthCookie(w, "__Host-sw_oauth")
	if w.Code != 200 || session == nil || csrf == nil || flow == nil || flow.MaxAge != -1 || !strings.Contains(w.Body.String(), `content="0;url=/runs?run=r1"`) {
		t.Fatalf("HTTP %d %s cookies=%v", w.Code, w.Body.String(), w.Header().Values("Set-Cookie"))
	}
	if !session.HttpOnly || !session.Secure || session.Value == f.raw || csrf.Value == f.csrf || csrf.HttpOnly || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid session cookie")
	}
	if _, err := f.store.AsOperator().LookupSession(t.Context(), f.raw); err == nil {
		t.Fatal("replaced session remains live")
	}
	r := httptest.NewRequest("GET", oauthOrigin+"/api/v1/auth/whoami", nil)
	r.AddCookie(session)
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"principal":"member"`) || !strings.Contains(w.Body.String(), `"team":"team"`) {
		t.Fatalf("session HTTP %d %s", w.Code, w.Body.String())
	}
	w = f.callback(query, f.flow)
	assertRefusedFlow(t, w, "/login?next=%2Fruns%3Frun%3Dr1&refused=expired")
	if count, _ := f.state(t); count != 1 || oauthCookie(w, "__Host-sw_session") != nil {
		t.Fatal("replayed code created another session")
	}
	for _, secret := range []string{f.provider.code, "private-provider-access-token", "private-client-secret", session.Value, csrf.Value, "private-provider-diagnostic"} {
		if strings.Contains(f.logs.String(), secret) {
			t.Fatalf("private OAuth fact entered logs: %s", secret)
		}
	}
	if !strings.Contains(f.logs.String(), "route=/auth/{provider}/callback") || !strings.Contains(f.logs.String(), "principal_id=member") {
		t.Fatalf("callback audit lacks verified account: %s", f.logs.String())
	}
}

func TestOAuthHTTPProofProperty(t *testing.T) {
	f := newOAuthHTTP(t)
	property := func(input []byte, mutation uint8) bool {
		query := url.Values{"state": {f.oauthState}, "code": {f.provider.code}}
		cookie := *f.flow
		cookies := []*http.Cookie{&cookie}
		forged := "forged-" + hex.EncodeToString(input)
		switch mutation % 9 {
		case 0:
			query.Set("state", forged)
		case 1:
			cookies = nil
		case 2:
			cookie.Value = base64.RawURLEncoding.EncodeToString([]byte(`{"provider":"google","state":"` + f.oauthState + `","verifier":"` + forged + `"}`))
		case 3:
			cookies = append(cookies, &http.Cookie{Name: cookie.Name, Value: forged})
		case 4:
			query.Add("state", forged)
		case 5:
			query.Add("code", forged)
		case 6:
			cookie.Value = forged
		case 7:
			query.Del("code")
		case 8:
			query.Set("error", forged)
		}
		before, expiry := f.state(t)
		w := f.callback(query, cookies...)
		after, renewed := f.state(t)
		return w.Code == 303 && strings.HasPrefix(w.Header().Get("Location"), "/login?") && before == after && expiry == renewed && f.provider.calls == 0 && oauthCookie(w, cookie.Name) == nil && oauthCookie(w, "__Host-sw_session") == nil && w.Header().Get("Cache-Control") == "no-store"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 90}); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthHTTPProviderAndStoreRefusals(t *testing.T) {
	for _, state := range []string{"rejected", "unavailable", "unverified", "redirect", "license", "missing store", "missing key"} {
		t.Run(state, func(t *testing.T) {
			f := newOAuthHTTP(t)
			reason := "unavailable"
			switch state {
			case "rejected":
				f.provider.failure, reason = state, "expired"
			case "unavailable":
				f.provider.failure = state
			case "unverified":
				f.provider.failure, reason = state, "email_unverified"
			case "redirect":
				r := httptest.NewRequest("GET", "https://foreign.test/auth/github/callback?"+url.Values{"state": {f.oauthState}, "code": {f.provider.code}}.Encode(), nil)
				r.AddCookie(f.flow)
				w := httptest.NewRecorder()
				f.handler.ServeHTTP(w, r)
				assertRefusedFlow(t, w, "/login?next=%2Fruns%3Frun%3Dr1&refused=failed")
				return
			case "license":
				f.handler = withOAuthGitHub(controller.New(f.store, slog.New(slog.NewTextHandler(f.logs, nil))), f.provider).Handler()
			case "missing store":
				if err := f.store.Close(); err != nil {
					t.Fatal(err)
				}
			case "missing key":
				f.restartWithCipher(t, nil)
				f.handler = withOAuthGitHub(f.server, f.provider).Handler()
			}
			w := f.callback(url.Values{"state": {f.oauthState}, "code": {f.provider.code}}, f.flow)
			assertRefusedFlow(t, w, "/login?next=%2Fruns%3Frun%3Dr1&refused="+reason)
			if oauthCookie(w, "__Host-sw_session") != nil || strings.Contains(w.Body.String(), "private-") || strings.Contains(f.logs.String(), "private-") {
				t.Fatalf("HTTP %d %s logs=%s", w.Code, w.Body.String(), f.logs.String())
			}
		})
	}
}

func TestOAuthHTTPWaitlistedSessionHasNoTeamAuthority(t *testing.T) {
	f := newOAuthHTTP(t)
	for _, query := range []string{`DELETE FROM memberships`, `UPDATE accounts SET active_team='',waitlisted_at=1,waitlist_reason='manual'`} {
		if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	w := f.callback(url.Values{"state": {f.oauthState}, "code": {f.provider.code}}, f.flow)
	cookie := oauthCookie(w, "__Host-sw_session")
	if w.Code != 200 || cookie == nil || cookie.Value == "" || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private-") || strings.Contains(f.logs.String(), "private-") || strings.Contains(f.logs.String(), cookie.Value) {
		t.Fatalf("HTTP %d %s logs=%s", w.Code, w.Body, f.logs)
	}
	session, err := f.store.AsOperator().LookupSession(t.Context(), cookie.Value)
	if err != nil || session.Team != "" || len(session.Scopes) != 0 {
		t.Fatalf("waitlisted session gained authority: %+v %v", session, err)
	}
	for _, path := range []string{"/api/v1/runs", "/api/v1/tokens", "/api/v1/operator/teams"} {
		r := httptest.NewRequest("GET", oauthOrigin+path, nil)
		r.Header.Set("Authorization", "Session "+cookie.Value)
		denied := httptest.NewRecorder()
		f.handler.ServeHTTP(denied, r)
		assertEngineError(t, denied, 403, apierr.Forbidden)
		if strings.Contains(denied.Body.String(), cookie.Value) {
			t.Fatal("refusal disclosed session")
		}
	}
}

func TestOAuthHTTPPKCEProperty(t *testing.T) {
	f := newOAuthHTTP(t)
	property := func(input []byte) bool {
		f.provider.diagnostic = "private-" + hex.EncodeToString(input)
		data, err := base64.RawURLEncoding.DecodeString(f.flow.Value)
		if err != nil {
			t.Fatal(err)
		}
		var flow map[string]string
		if err := json.Unmarshal(data, &flow); err != nil {
			t.Fatal(err)
		}
		flow["verifier"] = "forged-" + hex.EncodeToString(input)
		data, err = json.Marshal(flow)
		if err != nil {
			t.Fatal(err)
		}
		cookie := *f.flow
		cookie.Value = base64.RawURLEncoding.EncodeToString(data)
		before, expiry := f.state(t)
		w := f.callback(url.Values{"state": {f.oauthState}, "code": {f.provider.code}}, &cookie)
		after, renewed := f.state(t)
		return w.Code == 303 && w.Header().Get("Location") == "/login?next=%2Fruns%3Frun%3Dr1&refused=expired" && before == after && expiry == renewed && !f.provider.spent && oauthCookie(w, "__Host-sw_session") == nil && !strings.Contains(f.logs.String(), "private-")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthHTTPDeclineAndSafeNext(t *testing.T) {
	f := newOAuthHTTP(t)
	w := f.callback(url.Values{"state": {f.oauthState}, "error": {"private-provider-error"}}, f.flow)
	assertRefusedFlow(t, w, "/login?next=%2Fruns%3Frun%3Dr1&refused=cancelled")
	if f.provider.calls != 0 || oauthCookie(w, f.flow.Name) != nil || strings.Contains(w.Body.String(), "private-") {
		t.Fatal("declined callback disclosed provider text or changed the flow")
	}
	data, err := base64.RawURLEncoding.DecodeString(f.flow.Value)
	if err != nil {
		t.Fatal(err)
	}
	var flow map[string]string
	if err := json.Unmarshal(data, &flow); err != nil {
		t.Fatal(err)
	}
	flow["next"] = "https://foreign.test/private"
	data, err = json.Marshal(flow)
	if err != nil {
		t.Fatal(err)
	}
	cookie := *f.flow
	cookie.Value = base64.RawURLEncoding.EncodeToString(data)
	w = f.callback(url.Values{"state": {f.oauthState}, "code": {f.provider.code}}, &cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `content="0;url=/"`) || strings.Contains(w.Body.String(), "foreign.test") {
		t.Fatalf("unsafe next HTTP %d %s", w.Code, w.Body.String())
	}
}

func TestOAuthHTTPLinkCallbackCarriesProofWithoutIssuingSession(t *testing.T) {
	f := newOAuthHTTP(t)
	data, err := base64.RawURLEncoding.DecodeString(f.flow.Value)
	if err != nil {
		t.Fatal(err)
	}
	var flow map[string]string
	if err := json.Unmarshal(data, &flow); err != nil {
		t.Fatal(err)
	}
	flow["mode"] = "link"
	data, err = json.Marshal(flow)
	if err != nil {
		t.Fatal(err)
	}
	cookie := *f.flow
	cookie.Value = base64.RawURLEncoding.EncodeToString(data)
	before, expiry := f.state(t)
	w := f.callback(url.Values{"state": {f.oauthState}, "code": {f.provider.code}}, &cookie)
	after, renewed := f.state(t)
	continued := oauthCookie(w, f.flow.Name)
	if w.Code != 200 || continued == nil || f.provider.calls != 0 || oauthCookie(w, "__Host-sw_session") != nil || before != after || expiry != renewed {
		t.Fatalf("link callback HTTP %d %s", w.Code, w.Body.String())
	}
	data, err = base64.RawURLEncoding.DecodeString(continued.Value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &flow); err != nil {
		t.Fatal(err)
	}
	if flow["code"] != f.provider.code || flow["state"] != f.oauthState || flow["mode"] != "link" || !strings.Contains(w.Body.String(), "/auth/github/link/complete") || strings.Contains(w.Body.String(), f.provider.code) {
		t.Fatal("link callback lost proof or placed code in page")
	}
	w = f.callback(url.Values{"state": {f.oauthState}, "code": {f.provider.code}}, continued)
	assertRefusedFlow(t, w, "/account/sign-ins?refused=expired")
}

func TestOAuthHTTPRateLimit(t *testing.T) {
	f := newOAuthHTTP(t)
	for range 29 {
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, httptest.NewRequest("GET", oauthOrigin+"/auth/github/start", nil))
		if w.Code != 303 {
			t.Fatalf("start HTTP %d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, httptest.NewRequest("GET", oauthOrigin+"/auth/github/start", nil))
	assertRefusedFlow(t, w, "/login?refused=rate_limited")
	if w.Header().Get("Retry-After") != "60" || f.provider.calls != 0 {
		t.Fatal("invalid rate-limit response")
	}
}

func TestOAuthHTTPMethodsAndUnknownProvider(t *testing.T) {
	f := newOAuthHTTP(t)
	for _, path := range []string{"/auth/github/start", "/auth/github/callback"} {
		for _, method := range []string{"HEAD", "POST", "PUT"} {
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, httptest.NewRequest(method, oauthOrigin+path, nil))
			assertEngineError(t, w, 405, apierr.MethodNotAllowed)
		}
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, httptest.NewRequest("GET", oauthOrigin+"/auth/unknown/start", nil))
	assertRefusedFlow(t, w, "/login?refused=unavailable")
	if f.provider.calls != 0 {
		t.Fatal("invalid route reached provider")
	}
}
