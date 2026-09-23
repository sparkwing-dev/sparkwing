package web

import (
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
)

const (
	fakeInstallURL        = "https://github.example/apps/sparkwing/installations/new?state=app-state"
	fakeAppAuthorizeURL   = "https://github.example/login/oauth/authorize?state=app-state"
	fakeAppState          = "app-state"
	fakeAppVerifier       = "app-verifier"
	githubAppFlowCookie   = "__Host-sw_github_app"
	githubAppCallbackURI  = "https://dashboard.example/github/app/callback"
	githubAppTestSession  = "user-session"
	githubAppTestCSRF     = "csrf-user-session"
	githubAppTestDashHost = "https://dashboard.example"
)

type githubAppController struct {
	*httptest.Server

	mu             sync.Mutex
	completeStatus int
	completeBody   string
	calls          []githubAppCall
}

type githubAppCall struct {
	Path          string
	Authorization string
	Body          map[string]any
}

func newGitHubAppController(t *testing.T) *githubAppController {
	t.Helper()
	c := &githubAppController{completeStatus: http.StatusCreated}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.Close)
	return c
}

func (c *githubAppController) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch r.URL.Path {
	case "/api/v1/auth/session":
		_ = json.NewEncoder(w).Encode(sessionResp{
			Principal: "user:ada@example.com", Scopes: []string{"runs.read", "team.admin"},
			CSRFToken: "csrf-" + strings.TrimPrefix(r.Header.Get("Authorization"), "Session "),
			ExpiresAt: time.Now().Add(time.Hour).Unix(), UserID: "u1", Team: "acme",
		})
		return
	case "/api/v1/capabilities":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"teams":      map[string]bool{"enabled": true},
			"auth":       map[string][]string{"providers": {"github"}},
			"github_app": map[string]any{"slug": "sparkwing-test", "source_tokens": true},
		})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.calls = append(c.calls, githubAppCall{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: body})
	switch r.URL.Path {
	case "/api/v1/team/github-app/connect":
		_ = json.NewEncoder(w).Encode(githubAppConnectResp{
			InstallURL: fakeInstallURL, AuthorizeURL: fakeAppAuthorizeURL, State: fakeAppState, Verifier: fakeAppVerifier,
		})
	case "/api/v1/team/github-app/connect/complete":
		w.WriteHeader(c.completeStatus)
		if c.completeStatus == http.StatusCreated {
			_ = json.NewEncoder(w).Encode(map[string]any{"installation_id": 42, "account_login": "octo-org"})
			return
		}
		_, _ = w.Write([]byte(c.completeBody))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (c *githubAppController) refuseCompleteWith(status int, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completeStatus, c.completeBody = status, body
}

func (c *githubAppController) snapshot() []githubAppCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]githubAppCall{}, c.calls...)
}

func githubAppDashboard(t *testing.T) (http.Handler, *githubAppController) {
	t.Helper()
	ctrl := newGitHubAppController(t)
	return HandlerFromOptionsWithBundle(HandlerOptions{
		Backend:       &fakeBackend{caps: backend.Capabilities{Mode: "cluster"}},
		ControllerURL: ctrl.URL,
		Token:         "service-token",
		RequireLogin:  true,
	}, authTestBundle), ctrl
}

func githubAppFlowValue(flow githubAppFlow) string {
	raw, _ := json.Marshal(flow)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func startedFlow() githubAppFlow {
	return githubAppFlow{State: fakeAppState, Verifier: fakeAppVerifier, AuthorizeURL: fakeAppAuthorizeURL}
}

func connectRequest(csrfForm string) *http.Request {
	form := url.Values{}
	if csrfForm != "" {
		form.Set("csrf_token", csrfForm)
	}
	req := httptest.NewRequest(http.MethodPost, githubAppTestDashHost+"/github/app/connect", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", githubAppTestDashHost)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: githubAppTestSession})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: githubAppTestCSRF})
	return req
}

func githubAppReturn(handler http.Handler, path string, query url.Values, flow *githubAppFlow, session bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, githubAppTestDashHost+path+"?"+query.Encode(), nil)
	if flow != nil {
		req.AddCookie(&http.Cookie{Name: githubAppFlowCookie, Value: githubAppFlowValue(*flow)})
	}
	if session {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: githubAppTestSession})
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func flowFrom(t *testing.T, rec *httptest.ResponseRecorder) githubAppFlow {
	t.Helper()
	c := findCookie(rec.Result().Cookies(), githubAppFlowCookie)
	if c == nil || c.Value == "" {
		t.Fatalf("response set no flow cookie: %v", rec.Result().Cookies())
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		t.Fatal(err)
	}
	var flow githubAppFlow
	if err := json.Unmarshal(raw, &flow); err != nil {
		t.Fatal(err)
	}
	return flow
}

func TestGitHubAppConnectStartsTheFlowAsTheSignedInOwner(t *testing.T) {
	t.Parallel()
	handler, ctrl := githubAppDashboard(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, connectRequest(githubAppTestCSRF))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `content="0;url=`+template.HTMLEscapeString(fakeInstallURL)+`"`) {
		t.Fatalf("connect = %d %s, want a page that moves on to the install_url", rec.Code, rec.Body)
	}
	cookie := findCookie(rec.Result().Cookies(), githubAppFlowCookie)
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode ||
		cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge <= 0 || cookie.MaxAge > int(githubAppFlowTTL/time.Second) {
		t.Fatalf("flow cookie = %+v, want a short-lived Secure HttpOnly Lax host-only cookie", cookie)
	}
	if flow := flowFrom(t, rec); flow != startedFlow() {
		t.Errorf("flow cookie = %+v, want the controller's state, verifier and authorize_url", flow)
	}
	calls := ctrl.snapshot()
	if len(calls) != 1 || calls[0].Authorization != "Session "+githubAppTestSession ||
		calls[0].Body["redirect_uri"] != githubAppCallbackURI {
		t.Fatalf("controller calls = %+v, want one connect as the user's session with the dashboard callback", calls)
	}
}

func TestGitHubAppConnectRefusesAFormTheDashboardDidNotServe(t *testing.T) {
	t.Parallel()
	handler, ctrl := githubAppDashboard(t)
	crossSite := connectRequest(githubAppTestCSRF)
	crossSite.Header.Set("Origin", "https://attacker.example")
	for name, req := range map[string]*http.Request{
		"missing token": connectRequest(""),
		"wrong token":   connectRequest("csrf-someone-else"),
		"cross-site":    crossSite,
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("connect with %s = %d, want 403", name, rec.Code)
		}
		if findCookie(rec.Result().Cookies(), githubAppFlowCookie) != nil {
			t.Errorf("connect with %s set a flow cookie", name)
		}
	}
	if calls := ctrl.snapshot(); len(calls) != 0 {
		t.Fatalf("forged connects reached the controller: %+v", calls)
	}
}

func TestGitHubAppSetupRefusesAFlowThisBrowserDidNotStart(t *testing.T) {
	t.Parallel()
	handler, _ := githubAppDashboard(t)
	valid := url.Values{"installation_id": {"42"}, "setup_action": {"install"}, "state": {fakeAppState}}
	forged := url.Values{"installation_id": {"42"}, "setup_action": {"install"}, "state": {"someone-elses-state"}}
	badInstallation := url.Values{"installation_id": {"not-a-number"}, "setup_action": {"install"}, "state": {fakeAppState}}
	flow := startedFlow()
	for _, test := range []struct {
		name  string
		query url.Values
		flow  *githubAppFlow
	}{
		{"missing cookie", valid, nil},
		{"state mismatch", forged, &flow},
		{"no installation", badInstallation, &flow},
	} {
		rec := githubAppReturn(handler, "/github/app/setup", test.query, test.flow, false)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("%s: setup = %d to %q, want 400 with no redirect", test.name, rec.Code, rec.Header().Get("Location"))
		}
		// safety: a forged return must not discard the flow the browser has in flight.
		if c := findCookie(rec.Result().Cookies(), githubAppFlowCookie); c != nil {
			t.Errorf("%s: setup rewrote the flow cookie: %+v", test.name, c)
		}
	}
}

func TestGitHubAppSetupRemembersTheInstallationAndAuthorizes(t *testing.T) {
	t.Parallel()
	handler, _ := githubAppDashboard(t)
	flow := startedFlow()
	rec := githubAppReturn(handler, "/github/app/setup",
		url.Values{"installation_id": {"42"}, "setup_action": {"install"}, "state": {fakeAppState}}, &flow, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fakeAppAuthorizeURL {
		t.Fatalf("setup = %d %q, want 303 to the authorize_url", rec.Code, rec.Header().Get("Location"))
	}
	if got := flowFrom(t, rec); got.InstallationID != 42 || got.State != fakeAppState || got.Verifier != fakeAppVerifier {
		t.Fatalf("flow after setup = %+v, want installation 42 kept beside the state and verifier", got)
	}
}

func TestGitHubAppSetupExplainsAnInstallAwaitingApproval(t *testing.T) {
	t.Parallel()
	handler, _ := githubAppDashboard(t)
	flow := startedFlow()
	rec := githubAppReturn(handler, "/github/app/setup", url.Values{"setup_action": {"request"}, "state": {fakeAppState}}, &flow, false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "An org owner must approve the installation") {
		t.Fatalf("request setup = %d %s, want the approval page", rec.Code, rec.Body)
	}
	if c := findCookie(rec.Result().Cookies(), githubAppFlowCookie); c == nil || c.MaxAge >= 0 {
		t.Errorf("request setup kept the flow cookie: %+v", c)
	}

	update := githubAppReturn(handler, "/github/app/setup", url.Values{"installation_id": {"42"}, "setup_action": {"update"}}, nil, false)
	if update.Code != http.StatusSeeOther || update.Header().Get("Location") != githubAppSettingsPath {
		t.Fatalf("update from github.com = %d %q, want 303 to the settings page", update.Code, update.Header().Get("Location"))
	}
}

func TestGitHubAppCallbackRefusesAFlowThisBrowserDidNotStart(t *testing.T) {
	t.Parallel()
	handler, ctrl := githubAppDashboard(t)
	installed := startedFlow()
	installed.InstallationID = 42
	skippedSetup := startedFlow()
	valid := url.Values{"code": {"gh-code"}, "state": {fakeAppState}}
	for _, test := range []struct {
		name  string
		query url.Values
		flow  *githubAppFlow
	}{
		{"missing cookie", valid, nil},
		{"state mismatch", url.Values{"code": {"gh-code"}, "state": {"someone-elses-state"}}, &installed},
		{"installation only in the URL", url.Values{"code": {"gh-code"}, "state": {fakeAppState}, "installation_id": {"7"}}, &skippedSetup},
		{"no code", url.Values{"state": {fakeAppState}}, &installed},
	} {
		rec := githubAppReturn(handler, githubAppCallbackPath, test.query, test.flow, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: callback = %d, want 400", test.name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), githubAppCompletePath) {
			t.Errorf("%s: callback moved on to completion", test.name)
		}
	}
	if calls := ctrl.snapshot(); len(calls) != 0 {
		t.Fatalf("refused callbacks reached the controller: %+v", calls)
	}
}

func TestGitHubAppCallbackKeepsTheCodeAndMovesOnSameSite(t *testing.T) {
	t.Parallel()
	handler, ctrl := githubAppDashboard(t)
	flow := startedFlow()
	flow.InstallationID = 42
	rec := githubAppReturn(handler, githubAppCallbackPath, url.Values{"code": {"gh-code"}, "state": {fakeAppState}}, &flow, false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `content="0;url=/github/app/complete"`) {
		t.Fatalf("callback = %d %s, want a page that moves on to completion", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "gh-code") {
		t.Errorf("callback page carries the code")
	}
	if got := flowFrom(t, rec); got.Code != "gh-code" || got.InstallationID != 42 {
		t.Errorf("flow after callback = %+v, want the code kept with installation 42", got)
	}
	if calls := ctrl.snapshot(); len(calls) != 0 {
		t.Fatalf("callback without a session called the controller: %+v", calls)
	}
}

func TestGitHubAppCompleteBindsTheInstallationTheSetupRecorded(t *testing.T) {
	t.Parallel()
	handler, ctrl := githubAppDashboard(t)
	flow := startedFlow()
	flow.InstallationID, flow.Code = 42, "gh-code"
	rec := githubAppReturn(handler, githubAppCompletePath, url.Values{"installation_id": {"7"}}, &flow, true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/team/github?connected=octo-org" {
		t.Fatalf("complete = %d %q, want 303 to the settings page naming the account: %s",
			rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if c := findCookie(rec.Result().Cookies(), githubAppFlowCookie); c == nil || c.MaxAge >= 0 {
		t.Errorf("complete kept the flow cookie: %+v", c)
	}
	calls := ctrl.snapshot()
	if len(calls) != 1 || calls[0].Path != "/api/v1/team/github-app/connect/complete" ||
		calls[0].Authorization != "Session "+githubAppTestSession {
		t.Fatalf("controller calls = %+v, want one complete as the user's session", calls)
	}
	want := map[string]any{
		"state": fakeAppState, "verifier": fakeAppVerifier, "code": "gh-code",
		"installation_id": float64(42), "redirect_uri": githubAppCallbackURI,
	}
	for k, v := range want {
		if calls[0].Body[k] != v {
			t.Errorf("complete body %s = %v, want %v", k, calls[0].Body[k], v)
		}
	}
}

func TestGitHubAppCompleteRefusesWithoutAFinishedFlow(t *testing.T) {
	t.Parallel()
	handler, ctrl := githubAppDashboard(t)
	noCode := startedFlow()
	noCode.InstallationID = 42
	for name, flow := range map[string]*githubAppFlow{"missing cookie": nil, "no code": &noCode} {
		rec := githubAppReturn(handler, githubAppCompletePath, nil, flow, true)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: complete = %d, want 400", name, rec.Code)
		}
	}
	if calls := ctrl.snapshot(); len(calls) != 0 {
		t.Fatalf("unfinished flows reached the controller: %+v", calls)
	}
}

func TestGitHubAppCompleteExplainsEachRefusal(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status int
		body   string
		want   int
		says   string
	}{
		{
			http.StatusForbidden, `{"error":"link a GitHub sign-in to this account before connecting the GitHub App"}`,
			http.StatusForbidden, "Link your GitHub account to your Sparkwing account first",
		},
		{
			http.StatusForbidden, `{"error":"your GitHub account does not administer the account this installation belongs to"}`,
			http.StatusForbidden, "does not administer the account",
		},
		{
			http.StatusNotFound, `{"error":"GitHub reports no installation of the App by that id"}`,
			http.StatusNotFound, "no installation of the App",
		},
		{
			http.StatusConflict, `{"error":"this installation is connected to another team"}`,
			http.StatusConflict, "connected to another team",
		},
		{
			http.StatusBadGateway, `{"error":"internal server error"}`,
			http.StatusBadGateway, "GitHub could not be reached",
		},
	} {
		handler, ctrl := githubAppDashboard(t)
		ctrl.refuseCompleteWith(test.status, test.body)
		flow := startedFlow()
		flow.InstallationID, flow.Code = 42, "gh-code"
		rec := githubAppReturn(handler, githubAppCompletePath, nil, &flow, true)
		if rec.Code != test.want || !strings.Contains(rec.Body.String(), test.says) {
			t.Errorf("controller %d: page = %d, want %d saying %q: %s", test.status, rec.Code, test.want, test.says, rec.Body)
		}
		if rec.Header().Get("Location") != "" {
			t.Errorf("controller %d: page redirected to %q", test.status, rec.Header().Get("Location"))
		}
	}
}

func TestCapabilitiesCarryTheControllersGitHubApp(t *testing.T) {
	t.Parallel()
	handler, _ := githubAppDashboard(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedInRequest(http.MethodGet, "/api/v1/capabilities", ""))
	var caps backend.Capabilities
	if err := json.NewDecoder(rec.Body).Decode(&caps); err != nil {
		t.Fatal(err)
	}
	if caps.GitHubApp == nil || caps.GitHubApp.Slug != "sparkwing-test" || !caps.GitHubApp.SourceTokens {
		t.Fatalf("capabilities github_app = %+v, want the controller's App", caps.GitHubApp)
	}
}
