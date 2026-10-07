package controller_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth"
	"github.com/sparkwing-dev/sparkwing/internal/githubauth/githubtest"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth/googletest"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const (
	dashRedirect   = "http://localhost:4343/auth/google/callback"
	githubRedirect = "http://localhost:4343/auth/github/callback"
)

type identityFixture struct {
	t         *testing.T
	url       string
	store     *store.Store
	google    *googletest.Issuer
	github    *githubtest.Server
	admin     string
	srv       *controller.Server
	logsToken string
	lastPage  string
}

type fixtureOpts struct {
	store     *store.Store
	license   string
	key       ed25519.PublicKey
	configure func(*controller.Server)
	logger    *slog.Logger

	checkoutURL, checkoutToken string
}

func multiTeamLicense(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()
	pub, priv := licensetest.NewKey(t)
	now := time.Now()
	return licensetest.Sign(t, priv, licensetest.Terms{
		Features: []string{license.FeatureMultiTeam}, IssuedTo: "test",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
	}), pub
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	return newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub})
}

func newIdentityFixtureWith(t *testing.T, o fixtureOpts) *identityFixture {
	t.Helper()
	st := o.store
	if st == nil {
		var err error
		st, err = teststore.Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
	}
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	iss := googletest.New(t)
	gh := githubtest.New(t)
	key := o.key
	if key == nil {
		key, _ = licensetest.NewKey(t)
	}
	srv := controller.New(st, o.logger).EnableAuthFromStore().
		WithLicense(license.Resolve(o.license, key, time.Now(), nil)).
		WithGoogleSignIn(googleauth.New(iss.Config()), []string{dashRedirect}).
		WithGitHubSignIn(githubauth.New(gh.Config()), []string{githubRedirect}).
		WithBillingCheckout(o.checkoutURL, o.checkoutToken).
		WithDashboard(controller.Dashboard{})
	if o.configure != nil {
		o.configure(srv)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return &identityFixture{t: t, url: ts.URL, store: st, google: iss, github: gh, admin: admin, srv: srv}
}

func (f *identityFixture) call(method, path, auth string, body, out any) int {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.url+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			f.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode
}

type exchangeBody struct {
	SessionID string `json:"session_id"`
	User      struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
	ActiveTeam *struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	} `json:"active_team"`
	Waitlisted bool `json:"waitlisted"`
}

func (f *identityFixture) signIn(p googletest.Person) exchangeBody {
	f.t.Helper()
	var out exchangeBody
	if status := f.oauthSignIn("google", func(verifier string) string {
		return f.google.Code(p, verifier, dashRedirect)
	}, &out); status != http.StatusOK {
		f.t.Fatalf("google sign-in = %d", status)
	}
	return out
}

// safety: the browser surface derives its redirect URI from this host, so it is the one on the allowlist.
const dashHost = "localhost:4343"

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (f *identityFixture) browserGet(path string, cookies ...*http.Cookie) *browserResponse {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.url+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Host = dashHost
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return f.browserDo(req)
}

// safety: the browser helpers read and close each body here, so no test holds a body it could leave open.
type browserResponse struct {
	StatusCode int
	Header     http.Header
	cookies    []*http.Cookie
	body       string
}

func (r *browserResponse) Cookies() []*http.Cookie { return r.cookies }

func (f *identityFixture) browserDo(req *http.Request) *browserResponse {
	f.t.Helper()
	resp, err := noRedirects.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return &browserResponse{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies(), body: string(raw)}
}

func responseCookie(resp *browserResponse, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name && c.MaxAge >= 0 {
			return c
		}
	}
	return nil
}

func flowVerifier(t *testing.T, c *http.Cookie) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		t.Fatal(err)
	}
	var flow struct {
		Verifier string `json:"verifier"`
	}
	if err := json.Unmarshal(raw, &flow); err != nil {
		t.Fatal(err)
	}
	return flow.Verifier
}

func (f *identityFixture) oauthSignIn(provider string, code func(verifier string) string, out *exchangeBody) int {
	f.t.Helper()
	start := f.browserGet("/auth/" + provider + "/start")
	if start.StatusCode != http.StatusSeeOther {
		f.t.Fatalf("%s start = %d", provider, start.StatusCode)
	}
	authorize, err := url.Parse(start.Header.Get("Location"))
	if err != nil {
		f.t.Fatal(err)
	}
	if want := "http://" + dashHost + "/auth/" + provider + "/callback"; authorize.Query().Get("redirect_uri") != want {
		f.t.Fatalf("authorize URL %s does not carry the dashboard's callback", authorize)
	}
	flow := responseCookie(start, "__Host-sw_oauth")
	if flow == nil {
		f.t.Fatalf("%s start set no flow cookie", provider)
	}
	callback := f.browserGet("/auth/"+provider+"/callback?"+url.Values{
		"code": {code(flowVerifier(f.t, flow))}, "state": {authorize.Query().Get("state")},
	}.Encode(), flow)
	if callback.StatusCode != http.StatusOK {
		f.lastPage = responseBody(f.t, callback)
		return callback.StatusCode
	}
	session := responseCookie(callback, "__Host-sw_session")
	if session == nil {
		f.t.Fatalf("%s callback set no session cookie", provider)
	}
	if out != nil {
		if status := f.call("GET", "/api/v1/me", sessionAuth(session.Value), nil, out); status != http.StatusOK {
			f.t.Fatalf("me after sign-in = %d", status)
		}
		out.SessionID = session.Value
	}
	return callback.StatusCode
}

func sessionAuth(id string) string { return "Session " + id }

type whoami struct {
	Principal string   `json:"principal"`
	Scopes    []string `json:"scopes"`
	Team      string   `json:"team"`
	Role      string   `json:"role"`
}

func (f *identityFixture) whoami(auth string) whoami {
	f.t.Helper()
	var w whoami
	if code := f.call("GET", "/api/v1/auth/whoami", auth, nil, &w); code != http.StatusOK {
		f.t.Fatalf("whoami = %d", code)
	}
	return w
}

func person(sub, email, given string) googletest.Person {
	return googletest.Person{Subject: sub, Email: email, EmailVerified: true, Name: given + " Test", GivenName: given}
}

func splitLicense(t *testing.T, raw string) (payload, sig []byte) {
	t.Helper()
	p, s, ok := strings.Cut(raw, ".")
	if !ok {
		t.Fatalf("license %q has no signature half", raw)
	}
	var err error
	if payload, err = base64.RawURLEncoding.DecodeString(p); err != nil {
		t.Fatal(err)
	}
	if sig, err = base64.RawURLEncoding.DecodeString(s); err != nil {
		t.Fatal(err)
	}
	return payload, sig
}

func (f *identityFixture) githubExchange(p githubtest.Person, out *exchangeBody) int {
	f.t.Helper()
	return f.oauthSignIn("github", func(verifier string) string {
		return f.github.Code(p, verifier, githubRedirect)
	}, out)
}

func (f *identityFixture) signInGitHub(p githubtest.Person) exchangeBody {
	f.t.Helper()
	var out exchangeBody
	if status := f.githubExchange(p, &out); status != http.StatusOK {
		f.t.Fatalf("github exchange = %d", status)
	}
	return out
}

func ghPerson(id int64, login, email string) githubtest.Person {
	return githubtest.Person{
		ID: id, Login: login, Name: login + " Test",
		Emails: []githubtest.Email{{Email: email, Primary: true, Verified: true}},
	}
}

func sessionIDOf(auth string) string { return strings.TrimPrefix(auth, "Session ") }

func (f *identityFixture) csrfFor(auth string) string {
	f.t.Helper()
	var sess struct {
		CSRFToken string `json:"csrf_token"`
	}
	if code := f.call("GET", "/api/v1/auth/session", auth, nil, &sess); code != http.StatusOK {
		f.t.Fatalf("session = %d", code)
	}
	return sess.CSRFToken
}

func (f *identityFixture) browserSend(method, auth, path string, form url.Values, extra ...*http.Cookie) *browserResponse {
	f.t.Helper()
	csrf := ""
	if auth != "" {
		csrf = f.csrfFor(auth)
		if form != nil && !form.Has("csrf_token") {
			form.Set("csrf_token", csrf)
		}
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, f.url+path, body)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Host = dashHost
	if auth != "" {
		req.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: sessionIDOf(auth)})
		req.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://"+dashHost)
	}
	for _, c := range extra {
		req.AddCookie(c)
	}
	return f.browserDo(req)
}

func responseBody(_ *testing.T, resp *browserResponse) string {
	return html.UnescapeString(resp.body)
}
