package controller_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
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
)

const dashRedirect = "http://localhost:4343/auth/google/callback"

type identityFixture struct {
	t      *testing.T
	url    string
	store  *store.Store
	google *googletest.Issuer
	github *githubtest.Server
	admin  string
	srv    *controller.Server
	// logsToken is the log-deletion credential a deletion fixture minted.
	logsToken string
}

type fixtureOpts struct {
	license string
	key     ed25519.PublicKey
	// configure adds to the server before it starts serving.
	configure func(*controller.Server)
	logger    *slog.Logger
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
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
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
		WithGitHubSignIn(githubauth.New(gh.Config()), []string{dashRedirect})
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
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
		State        string `json:"state"`
		Verifier     string `json:"verifier"`
	}
	if code := f.call("POST", "/api/v1/auth/oauth/google/start", "",
		map[string]string{"redirect_uri": dashRedirect}, &start); code != http.StatusOK {
		f.t.Fatalf("start = %d", code)
	}
	u, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		f.t.Fatal(err)
	}
	if u.Query().Get("state") != start.State || u.Query().Get("redirect_uri") != dashRedirect {
		f.t.Fatalf("authorize_url %s does not carry the flow's state and redirect", start.AuthorizeURL)
	}
	var out exchangeBody
	code := f.google.Code(p, start.Verifier, dashRedirect)
	if status := f.call("POST", "/api/v1/auth/oauth/google/exchange", "", map[string]string{
		"code": code, "verifier": start.Verifier, "redirect_uri": dashRedirect,
	}, &out); status != http.StatusOK {
		f.t.Fatalf("exchange = %d", status)
	}
	return out
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

func (f *identityFixture) githubExchange(p githubtest.Person, out any) int {
	f.t.Helper()
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
		Verifier     string `json:"verifier"`
	}
	if code := f.call("POST", "/api/v1/auth/oauth/github/start", "",
		map[string]string{"redirect_uri": dashRedirect}, &start); code != http.StatusOK {
		f.t.Fatalf("github start = %d", code)
	}
	return f.call("POST", "/api/v1/auth/oauth/github/exchange", "", map[string]string{
		"code": f.github.Code(p, start.Verifier, dashRedirect), "verifier": start.Verifier, "redirect_uri": dashRedirect,
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
