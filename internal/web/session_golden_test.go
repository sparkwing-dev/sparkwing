package web

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestClusterDashboardSessionAndProxyGoldenPath(t *testing.T) {
	t.Parallel()

	type controllerState struct {
		sync.Mutex
		created        bool
		loginCalls     int
		logoutSessions []string
		sessionHeaders []string
		proxyAuth      string
		proxyCalls     int
	}
	state := &controllerState{}
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.Lock()
		defer state.Unlock()
		switch r.URL.Path {
		case "/api/v1/auth/bootstrap-needed":
			_ = json.NewEncoder(w).Encode(map[string]bool{"needed": !state.created})
		case "/api/v1/users":
			var body map[string]string
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil ||
				body["name"] != "admin" || body["password"] != "correct-horse" {
				http.Error(w, "bad bootstrap request", http.StatusBadRequest)
				return
			}
			state.created = true
			w.WriteHeader(http.StatusCreated)
		case "/api/v1/auth/login":
			var body map[string]string
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil ||
				!state.created || body["username"] != "admin" || body["password"] != "correct-horse" {
				http.Error(w, "bad login request", http.StatusUnauthorized)
				return
			}
			state.loginCalls++
			_ = json.NewEncoder(w).Encode(loginResp{
				SessionID: "session-" + strconv.Itoa(state.loginCalls),
				CSRFToken: "csrf-token",
				Principal: "admin",
				Scopes:    []string{"admin"},
				ExpiresAt: time.Now().Add(time.Hour).Unix(),
			})
		case "/api/v1/auth/session":
			state.sessionHeaders = append(state.sessionHeaders, r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(sessionResp{
				Principal: "admin",
				Scopes:    []string{"admin"},
				CSRFToken: "csrf-token",
				ExpiresAt: time.Now().Add(time.Hour).Unix(),
			})
		case "/api/v1/auth/logout":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			state.logoutSessions = append(state.logoutSessions, body["session_id"])
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/probe":
			state.proxyAuth = r.Header.Get("Authorization")
			state.proxyCalls++
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(controller.Close)

	dashboard := httptest.NewServer(HandlerFromOptions(HandlerOptions{
		ControllerURL: controller.URL,
		Token:         "service-token",
		RequireLogin:  true,
	}))
	t.Cleanup(dashboard.Close)
	client := dashboard.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	unauthenticated := newDashboardRequest(t, http.MethodGet, dashboard.URL+"/", nil)
	resp := doDashboardRequest(t, client, unauthenticated)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login?next=/" {
		t.Fatalf("unauthenticated dashboard = %d %q, want 303 to login", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp.Body.Close()

	unauthenticatedAPI := newDashboardRequest(t, http.MethodGet, dashboard.URL+"/api/v1/probe", nil)
	unauthenticatedAPI.Header.Set("Accept", "application/json")
	resp = doDashboardRequest(t, client, unauthenticatedAPI)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	loginPage := newDashboardRequest(t, http.MethodGet, dashboard.URL+"/login", nil)
	resp = doDashboardRequest(t, client, loginPage)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Create first admin") {
		t.Fatalf("fresh login page = %d %q, want bootstrap form", resp.StatusCode, body)
	}

	bootstrap := newDashboardFormRequest(t, dashboard.URL+"/login/bootstrap", url.Values{
		"username": {"admin"},
		"password": {"correct-horse"},
		"next":     {"/runs"},
	})
	resp = doDashboardRequest(t, client, bootstrap)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/runs" {
		t.Fatalf("bootstrap = %d %q, want 303 to /runs", resp.StatusCode, resp.Header.Get("Location"))
	}
	bootstrapCookies := resp.Cookies()
	resp.Body.Close()
	assertSessionCookies(t, bootstrapCookies, "session-1")

	logout := newDashboardRequest(t, http.MethodPost, dashboard.URL+"/logout", nil)
	addSessionCookie(t, logout, bootstrapCookies)
	resp = doDashboardRequest(t, client, logout)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("logout = %d %q, want 303 to /login", resp.StatusCode, resp.Header.Get("Location"))
	}
	assertClearedSessionCookies(t, resp.Cookies())
	resp.Body.Close()

	stolenBearer := newDashboardRequest(t, http.MethodGet, dashboard.URL+"/api/v1/probe", nil)
	stolenBearer.Header.Set("Accept", "application/json")
	stolenBearer.Header.Set("Authorization", "Bearer service-token")
	resp = doDashboardRequest(t, client, stolenBearer)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("service bearer after logout = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	login := newDashboardFormRequest(t, dashboard.URL+"/login", url.Values{
		"username": {"admin"},
		"password": {"correct-horse"},
		"next":     {"/cluster"},
	})
	resp = doDashboardRequest(t, client, login)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/cluster" {
		t.Fatalf("login = %d %q, want 303 to /cluster", resp.StatusCode, resp.Header.Get("Location"))
	}
	loginCookies := resp.Cookies()
	resp.Body.Close()
	assertSessionCookies(t, loginCookies, "session-2")

	proxy := newDashboardRequest(t, http.MethodGet, dashboard.URL+"/api/v1/probe", nil)
	proxy.Header.Set("Accept", "application/json")
	addSessionCookie(t, proxy, loginCookies)
	resp = doDashboardRequest(t, client, proxy)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated proxy = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	state.Lock()
	defer state.Unlock()
	if state.loginCalls != 2 {
		t.Errorf("controller login calls = %d, want bootstrap auto-login plus explicit login", state.loginCalls)
	}
	if len(state.logoutSessions) != 1 || state.logoutSessions[0] != "session-1" {
		t.Errorf("controller logout sessions = %v, want session-1", state.logoutSessions)
	}
	if len(state.sessionHeaders) != 1 || state.sessionHeaders[0] != "Session session-2" {
		t.Errorf("controller session headers = %v, want Session session-2", state.sessionHeaders)
	}
	if state.proxyAuth != "Bearer service-token" {
		t.Errorf("proxied Authorization = %q, want service token", state.proxyAuth)
	}
	if state.proxyCalls != 1 {
		t.Errorf("controller proxy calls = %d, want only the session-authenticated request", state.proxyCalls)
	}

	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(
			`<script>window.__SPARKWING_TOKEN__="__SPARKWING_TOKEN_MARKER__";window.__SPARKWING_API_URL__="__SPARKWING_API_URL_MARKER__";window.__SPARKWING_REQUIRE_LOGIN__="__SPARKWING_REQUIRE_LOGIN_MARKER__";</script>`,
		)},
	}
	recorder := httptest.NewRecorder()
	spaHandler(fs.FS(bundle), HandlerOptions{
		Token:         "service-token",
		APIURL:        "https://controller.example.test",
		ControllerURL: controller.URL,
		RequireLogin:  true,
	}).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if strings.Contains(recorder.Body.String(), "service-token") ||
		!strings.Contains(recorder.Body.String(), `window.__SPARKWING_TOKEN__="";`) ||
		!strings.Contains(recorder.Body.String(), `window.__SPARKWING_API_URL__="";`) ||
		!strings.Contains(recorder.Body.String(), `window.__SPARKWING_REQUIRE_LOGIN__="true";`) {
		t.Errorf("authenticated dashboard HTML exposed its service token or lost auth config: %s", recorder.Body.String())
	}

	sessionless := httptest.NewRecorder()
	spaHandler(fs.FS(bundle), HandlerOptions{
		Token:        "service-token",
		APIURL:       "https://controller.example.test",
		RequireLogin: true,
	}).ServeHTTP(
		sessionless,
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if !strings.Contains(sessionless.Body.String(), `window.__SPARKWING_TOKEN__="service-token";`) ||
		!strings.Contains(sessionless.Body.String(), `window.__SPARKWING_API_URL__="https://controller.example.test";`) ||
		!strings.Contains(sessionless.Body.String(), `window.__SPARKWING_REQUIRE_LOGIN__="false";`) {
		t.Errorf("sessionless dashboard lost its existing runtime token behavior: %s", sessionless.Body.String())
	}
}

func newDashboardRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func newDashboardFormRequest(t *testing.T, target string, form url.Values) *http.Request {
	t.Helper()
	req := newDashboardRequest(t, http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func doDashboardRequest(t *testing.T, client *http.Client, req *http.Request) *http.Response {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func addSessionCookie(t *testing.T, req *http.Request, cookies []*http.Cookie) {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == sessionCookieName {
			req.AddCookie(cookie)
			return
		}
	}
	t.Fatal("response did not include a session cookie")
}

func assertSessionCookies(t *testing.T, cookies []*http.Cookie, sessionID string) {
	t.Helper()
	values := map[string]string{}
	for _, cookie := range cookies {
		values[cookie.Name] = cookie.Value
	}
	if values[sessionCookieName] != sessionID || values[csrfCookieName] != "csrf-token" {
		t.Fatalf("session cookies = %v, want %s and csrf-token", values, sessionID)
	}
}

func assertClearedSessionCookies(t *testing.T, cookies []*http.Cookie) {
	t.Helper()
	cleared := map[string]bool{}
	for _, cookie := range cookies {
		if cookie.MaxAge < 0 && cookie.Value == "" {
			cleared[cookie.Name] = true
		}
	}
	if !cleared[sessionCookieName] || !cleared[csrfCookieName] {
		t.Fatalf("cleared cookies = %v, want session and CSRF deletion", cleared)
	}
}
