package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type githubConnectFake struct {
	consumeErr             error
	team                   store.Team
	subject                string
	seen                   capability.Authorized
	bound                  store.GitHubAppInstallation
	consumed               map[string]bool
	bindErr                error
	reads, consumes, binds int
}

func (f *githubConnectFake) Team() store.Team { return f.team }
func (f *githubConnectFake) LinkedGitHubSubject(_ context.Context, a capability.Authorized) (string, error) {
	f.reads++
	f.seen = a
	return f.subject, nil
}

func (f *githubConnectFake) ConsumeGitHubConnect(_ context.Context, a capability.Authorized, nonce string, expires time.Time) error {
	f.consumes++
	f.seen = a
	if f.consumeErr != nil {
		return f.consumeErr
	}
	if nonce == "" || expires.Before(time.Now()) || f.consumed[nonce] {
		return &apierr.Error{Code: apierr.Forbidden, Message: "connection state unavailable"}
	}
	f.consumed[nonce] = true
	return nil
}

func (f *githubConnectFake) BindAuthorizedGitHubInstallation(_ context.Context, a capability.Authorized, row store.GitHubAppInstallation) (store.GitHubAppInstallation, error) {
	f.binds++
	f.seen, f.bound = a, row
	row.Team = f.team
	return row, f.bindErr
}

type githubBrowserFixture struct {
	s             *Server
	writer        *githubConnectFake
	h             http.Handler
	raw, csrf     string
	providerCalls int
}

func newGitHubBrowserFixture(t *testing.T) *githubBrowserFixture {
	t.Helper()
	e, _ := engineRuntimeStore(t)
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','owner',1)`)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithSecretsCipher(cipher).
		WithDeployment(deploymenttest.Team(t)).WithDashboard(Dashboard{})
	raw, csrf, _, err := e.Store.CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	f := &githubBrowserFixture{s: s, raw: raw, csrf: csrf, writer: &githubConnectFake{team: "a", subject: "42", consumed: make(map[string]bool)}}
	cfg := githubapp.Config{AppID: 1, Slug: "test", ClientID: "client", ClientSecret: "private-provider-secret", PrivateKey: key, HTTP: &http.Client{Transport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
		f.providerCalls++
		var body string
		switch r.Method + " " + r.URL.Path {
		case "POST /login/oauth/access_token":
			body = `{"access_token":"provider-token","token_type":"bearer"}`
		case "GET /user":
			body = `{"id":42,"login":"owner"}`
		case "GET /user/installations":
			body = `{"installations":[{"id":7,"app_id":1,"account":{"id":42,"login":"owner","type":"User"}}]}`
		case "GET /app/installations/7":
			body = `{"id":7,"app_id":1,"account":{"id":42,"login":"owner","type":"User"}}`
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL)
			return nil, errors.New("unexpected provider request")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	client := githubapp.New(cfg)
	s.githubApp = &githubAppState{client: client, stateKey: client.StateKey()}
	s.identity.redirectURIs = []string{"https://controller.test/github/app/callback"}
	s.WithGitHubConnectWriter(func(context.Context, store.Team) (GitHubConnectWriter, error) { return f.writer, nil })
	f.h = s.Handler()
	return f
}

func (f *githubBrowserFixture) request(method, path string, values url.Values, flow *githubAppFlow, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://controller.test"+path, strings.NewReader(values.Encode()))
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.raw})
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://controller.test")
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: f.csrf})
	}
	if flow != nil {
		value, _ := json.Marshal(flow)
		r.AddCookie(&http.Cookie{Name: githubAppFlowCookieName, Value: base64.RawURLEncoding.EncodeToString(value)})
	}
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *githubBrowserFixture) start(t *testing.T, existing bool) githubAppFlow {
	t.Helper()
	path := "/github/app/connect"
	if existing {
		path += "/existing"
	}
	w := f.request("POST", path, url.Values{"csrf_token": {f.csrf}}, nil, nil)
	if w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	return githubBrowserFlowCookie(t, w)
}

func githubBrowserFlowCookie(t *testing.T, w *httptest.ResponseRecorder) githubAppFlow {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name != githubAppFlowCookieName {
			continue
		}
		value, err := base64.RawURLEncoding.DecodeString(c.Value)
		if err != nil {
			t.Fatal(err)
		}
		var flow githubAppFlow
		if err := json.Unmarshal(value, &flow); err != nil {
			t.Fatal(err)
		}
		return flow
	}
	t.Fatal("connection flow cookie missing")
	return githubAppFlow{}
}

func TestGitHubBrowserConnectionAuthorityProperty(t *testing.T) {
	f := newGitHubBrowserFixture(t)
	property := func(input []byte, phase uint8) bool {
		hint := base64.RawURLEncoding.EncodeToString(input)
		values := url.Values{"csrf_token": {f.csrf}}
		want := 403
		f.writer.team = "a"
		engineSQL(t, f.s.store, `UPDATE memberships SET role='owner' WHERE team='a' AND account_id='member'`)
		mutate := func(r *http.Request) { r.Header.Set("X-Sparkwing-Team", hint) }
		switch phase % 8 {
		case 0:
			engineSQL(t, f.s.store, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='member'`)
		case 1:
			values.Set("csrf_token", "forged-"+hint)
		case 2:
			mutate = func(r *http.Request) { r.Header.Set("Origin", "https://foreign.test") }
		case 3:
			mutate = func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.raw}) }
			want = 401
		case 4:
			mutate = func(r *http.Request) { r.Header.Set("Authorization", "Session "+f.raw) }
			want = 401
		case 5:
			f.writer.team = "foreign"
			want = 500
		case 6:
			values.Set("team", hint)
			want = 400
		case 7:
			values["csrf_token"] = []string{f.csrf, f.csrf}
		}
		before := f.writer.reads
		w := f.request("POST", "/github/app/connect", values, nil, mutate)
		if w.Code != want || f.writer.reads != before || f.providerCalls != 0 || f.writer.consumes != 0 || f.writer.binds != 0 {
			t.Logf("phase %d = %d %s, want %d", phase%8, w.Code, w.Body, want)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubBrowserConnectionProviderAndReplay(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "install", true: "select"}[existing], func(t *testing.T) {
			f := newGitHubBrowserFixture(t)
			flow := f.start(t, existing)
			if !existing {
				query := url.Values{"state": {flow.State}, "installation_id": {"7"}, "setup_action": {"install"}}
				w := f.request("GET", "/github/app/setup?"+query.Encode(), nil, &flow, func(r *http.Request) {
					r.Header.Set("Cookie", githubAppFlowCookieName+"="+r.CookiesNamed(githubAppFlowCookieName)[0].Value)
				})
				if w.Code != 303 {
					t.Fatalf("setup = %d %s", w.Code, w.Body)
				}
				flow = githubBrowserFlowCookie(t, w)
			}
			query := url.Values{"state": {flow.State}, "code": {"code"}}
			callback := f.request("GET", githubAppCallbackPath+"?"+query.Encode(), nil, &flow, nil)
			if callback.Code != 200 {
				t.Fatalf("callback = %d %s", callback.Code, callback.Body)
			}
			flow = githubBrowserFlowCookie(t, callback)
			path, method, values := githubAppCompletePath, "GET", url.Values{}
			if existing {
				w := f.request("GET", githubAppAvailablePath, nil, &flow, nil)
				if w.Code != 200 {
					t.Fatalf("picker = %d %s", w.Code, w.Body)
				}
				flow = githubBrowserFlowCookie(t, w)
				path, method, values = "/github/app/select", "POST", url.Values{"csrf_token": {f.csrf}, "installation_id": {"7"}}
			}
			w := f.request(method, path, values, &flow, nil)
			if w.Code != 303 || f.writer.binds != 1 || f.writer.bound.InstallationID != 7 || f.writer.bound.GitHubUserID != 42 || f.writer.bound.ConnectedBy != "member" || f.writer.seen.Target.Team != "a" || f.writer.seen.Principal.Authority.Kind != capability.AuthoritySession {
				t.Fatalf("bind = %d %s, writer %+v", w.Code, w.Body, f.writer)
			}
			calls := f.providerCalls
			w = f.request(method, path, values, &flow, nil)
			if w.Code < 400 || f.writer.binds != 1 || f.providerCalls != calls {
				t.Fatalf("replay = %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestGitHubBrowserConnectionRealStore(t *testing.T) {
	f := newGitHubBrowserFixture(t)
	engineSQL(t, f.s.store, `INSERT INTO identities(provider,subject,account_id,email,email_verified,created_at) VALUES ('github','42','member','member@example.test',1,1)`)
	f.s.WithGitHubConnectWriter(nil)
	f.h = f.s.Handler()
	flow := f.start(t, false)
	flow.Code, flow.InstallationID = "code", 7
	w := f.request("GET", githubAppCompletePath, nil, &flow, nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("complete = %d %s", w.Code, w.Body)
	}
	tenant, err := f.s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.GitHubAppInstallation(t.Context(), 7)
	if err != nil || row.ConnectedBy != "member" || row.GitHubUserID != 42 {
		t.Fatalf("installation = %+v: %v", row, err)
	}
	calls := f.providerCalls
	w = f.request("GET", githubAppCompletePath, nil, &flow, nil)
	if w.Code < 400 || f.providerCalls != calls {
		t.Fatalf("replay = %d %s, provider calls %d -> %d", w.Code, w.Body, calls, f.providerCalls)
	}
}

func TestGitHubBrowserConnectionStateProperty(t *testing.T) {
	f := newGitHubBrowserFixture(t)
	flow := f.start(t, false)
	flow.Code, flow.InstallationID = "code", 7
	property := func(input []byte, phase uint8) bool {
		altered := flow
		hint := base64.RawURLEncoding.EncodeToString(input)
		mutate := func(*http.Request) {}
		switch phase % 5 {
		case 0:
			altered.State += "x" + hint
		case 1:
			altered.Verifier += "x" + hint
		case 2:
			mutate = func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: githubAppFlowCookieName, Value: "duplicate" + hint})
			}
		case 3, 4:
			var state githubConnectState
			if !openFlowState(f.s.githubApp.stateKey, altered.State, &state) {
				return false
			}
			if phase%5 == 3 {
				state.Team = "foreign-" + hint
			} else {
				state.Account = "foreign-" + hint
			}
			var err error
			altered.State, err = f.s.githubApp.signState(state)
			if err != nil {
				t.Fatal(err)
			}
		}
		w := f.request("GET", githubAppCompletePath, nil, &altered, mutate)
		return w.Code == 400 || w.Code == 403
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96}); err != nil {
		t.Fatal(err)
	}
	if f.providerCalls != 0 || f.writer.consumes != 0 || f.writer.binds != 0 {
		t.Fatalf("invalid state crossed provider/commit boundary: %+v", f.writer)
	}
}

func TestGitHubBrowserConnectionCommitRefusal(t *testing.T) {
	f := newGitHubBrowserFixture(t)
	flow := f.start(t, false)
	flow.Code, flow.InstallationID = "code", 7
	f.writer.bindErr = &apierr.Error{Code: apierr.Forbidden, Message: "session authority changed"}
	w := f.request("GET", githubAppCompletePath, nil, &flow, nil)
	if w.Code != 403 || f.writer.binds != 1 || strings.Contains(w.Body.String(), "connected=") || strings.Contains(w.Body.String(), "provider-token") {
		t.Fatalf("bind conflict = %d %s", w.Code, w.Body)
	}
	flow = f.start(t, false)
	flow.Code, flow.InstallationID = "code", 7
	f.writer.bindErr = errors.New("private-provider-secret")
	w = f.request("GET", githubAppCompletePath, nil, &flow, nil)
	if w.Code != 500 || strings.Contains(w.Body.String(), "private-provider-secret") {
		t.Fatalf("private failure = %d %s", w.Code, w.Body)
	}
}

func TestGitHubBrowserConnectionRequiresTransaction(t *testing.T) {
	f := newGitHubBrowserFixture(t)
	f.s.WithGitHubConnectWriter(func(context.Context, store.Team) (GitHubConnectWriter, error) { return nil, nil })
	w := f.request("POST", "/github/app/connect", url.Values{"csrf_token": {f.csrf}}, nil, nil)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "policy_missing") || f.providerCalls != 0 {
		t.Fatalf("missing writer = %d %s", w.Code, w.Body)
	}
}

func TestGitHubBrowserConnectionUsesStoreTransactions(t *testing.T) {
	f := newGitHubBrowserFixture(t)
	f.s.resolveGitHubConnectWriter = nil
	w := f.request("POST", "/github/app/connect", url.Values{"csrf_token": {f.csrf}}, nil, nil)
	if w.Code != http.StatusForbidden || f.providerCalls != 0 {
		t.Fatalf("unlinked account = %d %s", w.Code, w.Body)
	}
	engineSQL(t, f.s.store, `INSERT INTO identities(provider,subject,account_id,created_at) VALUES ('github','42','member',1)`)
	flow := f.start(t, false)
	flow.InstallationID = 7
	query := url.Values{"state": {flow.State}, "code": {"code"}}
	w = f.request("GET", githubAppCallbackPath+"?"+query.Encode(), nil, &flow, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("callback = %d %s", w.Code, w.Body)
	}
	flow = githubBrowserFlowCookie(t, w)
	w = f.request("GET", githubAppCompletePath, nil, &flow, nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("complete = %d %s", w.Code, w.Body)
	}
	tenant, err := f.s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := tenant.GitHubAppInstallation(t.Context(), 7)
	if err != nil || bound.Team != "a" || bound.ConnectedBy != "member" || bound.GitHubUserID != 42 {
		t.Fatalf("committed installation = %+v: %v", bound, err)
	}
	calls := f.providerCalls
	w = f.request("GET", githubAppCompletePath, nil, &flow, nil)
	if w.Code < 400 || f.providerCalls != calls || f.writer.reads != 0 || f.writer.consumes != 0 || f.writer.binds != 0 {
		t.Fatalf("replay = %d %s, provider calls %d -> %d, fake %+v", w.Code, w.Body, calls, f.providerCalls, f.writer)
	}
}

func TestGitHubBrowserConnectionPrivateConsumeFailure(t *testing.T) {
	f := newGitHubBrowserFixture(t)
	flow := f.start(t, false)
	flow.Code, flow.InstallationID = "code", 7
	f.writer.consumeErr = errors.New("private-database-password")
	w := f.request("GET", githubAppCompletePath, nil, &flow, nil)
	if w.Code != 500 || f.providerCalls != 0 || f.writer.binds != 0 || strings.Contains(w.Body.String(), "private-database-password") {
		t.Fatalf("consume failure = %d %s", w.Code, w.Body)
	}
}
