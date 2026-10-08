package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func (f *browserFixture) loginPage(next string) (csrf, page string) {
	f.t.Helper()
	rec := f.serve(httptest.NewRequest(http.MethodGet, dashOrigin+"/login?next="+url.QueryEscape(next), nil))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("login page = %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "__Host-sw_csrf" {
			csrf = c.Value
		}
	}
	return csrf, rec.Body.String()
}

func (f *browserFixture) postForm(path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, dashOrigin+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", dashOrigin)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return f.serve(req)
}

func setCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestBrowserLoginSetsTheSessionCookies(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	csrf, page := f.loginPage("/runs?run=r1")
	if csrf == "" || !strings.Contains(page, `name="csrf_token" value="`+csrf+`"`) {
		t.Fatalf("login form does not carry its CSRF cookie's token: %s", page)
	}
	rec := f.postForm("/login", url.Values{
		"username": {"alice"}, "password": {"correct-horse-battery"}, "next": {"/runs?run=r1"}, "csrf_token": {csrf},
	}, &http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/runs?run=r1" {
		t.Fatalf("login = %d %q, want 303 to next", rec.Code, rec.Header().Get("Location"))
	}
	session := setCookie(rec, "__Host-sw_session")
	token := setCookie(rec, "__Host-sw_csrf")
	if session == nil || !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode ||
		session.Path != "/" || session.Domain != "" || session.MaxAge <= 0 {
		t.Fatalf("session cookie = %+v, want HttpOnly Secure Lax host-only and persistent", session)
	}
	if token == nil || token.HttpOnly || !token.Secure || token.SameSite != http.SameSiteStrictMode || token.Value == csrf {
		t.Fatalf("csrf cookie = %+v, want a readable Strict cookie carrying the session's own token", token)
	}

	whoami := httptest.NewRequest(http.MethodGet, dashOrigin+"/api/v1/auth/whoami", nil)
	whoami.AddCookie(session)
	if got := f.serve(whoami); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"alice"`) {
		t.Fatalf("whoami with the new cookie = %d %s", got.Code, got.Body)
	}
}

func TestBrowserLoginRefusesWithoutTheFormToken(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	csrf, _ := f.loginPage("/")
	creds := url.Values{"username": {"alice"}, "password": {"correct-horse-battery"}}
	cases := map[string]func() *httptest.ResponseRecorder{
		"no token": func() *httptest.ResponseRecorder {
			return f.postForm("/login", creds, &http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
		},
		"a token that is not the cookie's": func() *httptest.ResponseRecorder {
			form := url.Values{"username": creds["username"], "password": creds["password"], "csrf_token": {"forged"}}
			return f.postForm("/login", form, &http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
		},
		"another origin": func() *httptest.ResponseRecorder {
			form := url.Values{"username": creds["username"], "password": creds["password"], "csrf_token": {csrf}}
			req := httptest.NewRequest(http.MethodPost, dashOrigin+"/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "https://evil.example")
			req.AddCookie(&http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
			return f.serve(req)
		},
	}
	for name, post := range cases {
		if rec := post(); rec.Code != http.StatusForbidden || setCookie(rec, "__Host-sw_session") != nil {
			t.Errorf("%s = %d, want 403 and no session", name, rec.Code)
		}
	}
}

func TestBrowserLoginAnswersAWrongPasswordOnThePage(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	csrf, _ := f.loginPage("/")
	rec := f.postForm("/login", url.Values{
		"username": {"alice"}, "password": {"wrong"}, "csrf_token": {csrf},
	}, &http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Invalid username or password.") ||
		setCookie(rec, "__Host-sw_session") != nil {
		t.Fatalf("wrong password = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `value="`+csrf+`"`) {
		t.Fatal("the re-rendered form lost the browser's CSRF token")
	}
}

func TestBrowserLoginKeepsNextOnThisOrigin(t *testing.T) {
	for next, want := range map[string]string{
		"/runs?run=r1":         "/runs?run=r1",
		"https://evil.example": "/",
		"//evil.example/x":     "/",
		`/\evil.example`:       "/",
		"/runs#frag":           "/",
		"runs":                 "/",
	} {
		f := newBrowserFixture(t, &controller.Dashboard{})
		csrf, _ := f.loginPage(next)
		rec := f.postForm("/login", url.Values{
			"username": {"alice"}, "password": {"correct-horse-battery"}, "next": {next}, "csrf_token": {csrf},
		}, &http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("next %q redirected to %q, want %q", next, got, want)
		}
	}
}

func TestBrowserSignedInLoginPageRedirectsAndAStaleCookieIsCleared(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	req := httptest.NewRequest(http.MethodGet, dashOrigin+"/login?next=%2Fcrons", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: f.sessID})
	if rec := f.serve(req); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/crons" {
		t.Fatalf("signed-in login page = %d %q, want 303 /crons", rec.Code, rec.Header().Get("Location"))
	}
	stale := httptest.NewRequest(http.MethodGet, dashOrigin+"/login", nil)
	stale.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: "swses_gone"})
	rec := f.serve(stale)
	if rec.Code != http.StatusOK || !clearsCookie(rec, "__Host-sw_session") {
		t.Fatalf("stale cookie on the login page = %d %v", rec.Code, rec.Header().Values("Set-Cookie"))
	}
}

func TestBrowserLoginEndsTheBrowsersEarlierSession(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	csrf, _ := f.loginPage("/")
	rec := f.postForm("/login", url.Values{
		"username": {"alice"}, "password": {"correct-horse-battery"}, "csrf_token": {csrf},
	}, &http.Cookie{Name: "__Host-sw_csrf", Value: csrf}, &http.Cookie{Name: "__Host-sw_session", Value: f.sessID})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login = %d", rec.Code)
	}
	old := httptest.NewRequest(http.MethodGet, dashOrigin+"/api/v1/auth/whoami", nil)
	old.Header.Set("Authorization", "Session "+f.sessID)
	if got := f.serve(old); got.Code != http.StatusUnauthorized {
		t.Fatalf("the replaced session = %d, want 401", got.Code)
	}
}

func TestBrowserLogoutNeedsTheSessionsTokenAndEndsIt(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	cookies := []*http.Cookie{
		{Name: "__Host-sw_session", Value: f.sessID},
		{Name: "__Host-sw_csrf", Value: "planted"},
	}
	if rec := f.postForm("/logout", url.Values{"csrf_token": {"planted"}}, cookies...); rec.Code != http.StatusForbidden {
		t.Fatalf("logout with a token the session never issued = %d, want 403", rec.Code)
	}
	var live int
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&live); err != nil || live != 1 {
		t.Fatalf("sessions after a refused logout = %d (%v), want 1", live, err)
	}
	cookies[1].Value = f.csrf
	rec := f.postForm("/logout", url.Values{"csrf_token": {f.csrf}}, cookies...)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" ||
		!clearsCookie(rec, "__Host-sw_session") || !clearsCookie(rec, "__Host-sw_csrf") {
		t.Fatalf("logout = %d %q %v", rec.Code, rec.Header().Get("Location"), rec.Header().Values("Set-Cookie"))
	}
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("sessions after logout = %d (%v), want 0", live, err)
	}
}

func TestBrowserBootstrapCreatesTheFirstAdminOnAnOpenController(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil).WithDashboard(controller.Dashboard{})
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	f := &browserFixture{t: t, st: st, srv: srv, h: srv.Handler()}

	csrf, page := f.loginPage("/runs")
	if !strings.Contains(page, `action="/login/bootstrap"`) {
		t.Fatalf("a fresh open controller offers no first-admin form: %s", page)
	}
	rec := f.postForm("/login/bootstrap", url.Values{
		"username": {"admin"}, "password": {"correct-horse"}, "next": {"/runs"}, "csrf_token": {csrf},
	}, &http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/runs" || setCookie(rec, "__Host-sw_session") == nil {
		t.Fatalf("bootstrap = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	users, err := st.ListUsers()
	if err != nil || len(users) != 1 || users[0].Name != "admin" {
		t.Fatalf("users = %+v (%v)", users, err)
	}

	again := f.postForm("/login/bootstrap", url.Values{
		"username": {"second"}, "password": {"correct-horse"}, "csrf_token": {csrf},
	}, &http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
	if again.Code != http.StatusConflict {
		t.Fatalf("a second bootstrap = %d, want 409", again.Code)
	}
	if users, _ := st.ListUsers(); len(users) != 1 {
		t.Fatalf("a closed bootstrap created a user: %+v", users)
	}
}

func TestBrowserBootstrapIsNotOfferedWhenAuthIsOn(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	if _, err := f.st.DB().Exec(`DELETE FROM users`); err != nil {
		t.Fatal(err)
	}
	csrf, page := f.loginPage("/")
	if strings.Contains(page, "Create first admin") {
		t.Fatal("an authenticated controller offers the first-admin form")
	}
	rec := f.postForm("/login/bootstrap", url.Values{
		"username": {"mallory"}, "password": {"correct-horse"}, "csrf_token": {csrf},
	}, &http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
	if rec.Code != http.StatusConflict {
		t.Fatalf("bootstrap on an authenticated controller = %d, want 409", rec.Code)
	}
	var n int
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("users = %d (%v), want 0", n, err)
	}
}

func TestBrowserRuntimeConfigCannotBreakOutOfItsScript(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{Version: "</script><script>alert(1)</script> "})
	rec := f.serve(httptest.NewRequest(http.MethodGet, dashOrigin+"/sparkwing-runtime.js", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "</script>") || strings.Contains(body, " ") ||
		!strings.Contains(body, `window.__SPARKWING_REQUIRE_LOGIN__="true"`) {
		t.Fatalf("runtime config = %d %q", rec.Code, body)
	}
	if strings.Contains(body, f.token) || strings.Contains(body, f.sessID) {
		t.Fatal("runtime config carries a credential")
	}
}

// safety: a refused sign-in is the record an operator looks for after a password-guessing run, so the browser's
// sign-in routes write the same audit line and request id the API does.
func TestBrowserSignInRoutesAreAudited(t *testing.T) {
	f := newBrowserFixture(t, &controller.Dashboard{})
	csrf, _ := f.loginPage("/")
	rec := f.postForm("/login", url.Values{
		"username": {"alice"}, "password": {"wrong"}, "csrf_token": {csrf},
	}, &http.Cookie{Name: "__Host-sw_csrf", Value: csrf})
	id := rec.Header().Get("X-Request-Id")
	if rec.Code != http.StatusUnauthorized || id == "" {
		t.Fatalf("wrong password = %d with request id %q, want 401 and an id", rec.Code, id)
	}
	if line := auditLine(f.logs.String(), id); !strings.Contains(line, "route=/login") || !strings.Contains(line, "status=401") {
		t.Fatalf("no audit record for the refused sign-in %s: %s", id, f.logs.String())
	}

	logout := f.postForm("/logout", url.Values{"csrf_token": {f.csrf}},
		&http.Cookie{Name: "__Host-sw_session", Value: f.sessID}, &http.Cookie{Name: "__Host-sw_csrf", Value: f.csrf})
	line := auditLine(f.logs.String(), logout.Header().Get("X-Request-Id"))
	if !strings.Contains(line, "route=/logout") || !strings.Contains(line, "principal_id=alice") {
		t.Fatalf("logout audit record does not name the signed-out principal: %q", line)
	}
}

func auditLine(logs, requestID string) string {
	for _, line := range strings.Split(logs, "\n") {
		if requestID != "" && strings.Contains(line, "msg=audit") && strings.Contains(line, "request_id="+requestID) {
			return line
		}
	}
	return ""
}
