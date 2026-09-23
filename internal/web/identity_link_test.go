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
	fakeLinkAuthorizeURL = "https://github.example/login/oauth/authorize?state=link-state"
	fakeLinkState        = "link-state"
	fakeLinkVerifier     = "link-verifier"
	linkCallbackURI      = "https://dashboard.example/auth/github/callback"
	oauthFlowCookie      = "__Host-sw_oauth"
)

type linkController struct {
	*httptest.Server

	mu             sync.Mutex
	completeStatus int
	completeBody   string
	calls          []githubAppCall
}

func newLinkController(t *testing.T) *linkController {
	t.Helper()
	c := &linkController{completeStatus: http.StatusCreated}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.Close)
	return c
}

func (c *linkController) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch r.URL.Path {
	case "/api/v1/auth/session":
		_ = json.NewEncoder(w).Encode(sessionResp{
			Principal: "user:ada@example.com", Scopes: []string{"runs.read"},
			CSRFToken: "csrf-" + strings.TrimPrefix(r.Header.Get("Authorization"), "Session "),
			ExpiresAt: time.Now().Add(time.Hour).Unix(), UserID: "u1", Team: "acme",
		})
		return
	case "/api/v1/capabilities":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"teams": map[string]bool{"enabled": true},
			"auth":  map[string][]string{"providers": {"google", "github"}},
		})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.calls = append(c.calls, githubAppCall{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: body})
	switch r.URL.Path {
	case "/api/v1/me/identities/github/link":
		_ = json.NewEncoder(w).Encode(oauthStartResp{
			AuthorizeURL: fakeLinkAuthorizeURL, State: fakeLinkState, Verifier: fakeLinkVerifier,
		})
	case "/api/v1/me/identities/github/link/complete":
		w.WriteHeader(c.completeStatus)
		if c.completeStatus == http.StatusCreated {
			_ = json.NewEncoder(w).Encode(map[string]any{"provider": "github", "email": "octo@example.com"})
			return
		}
		_, _ = w.Write([]byte(c.completeBody))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (c *linkController) refuseCompleteWith(status int, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completeStatus, c.completeBody = status, body
}

func (c *linkController) snapshot() []githubAppCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]githubAppCall{}, c.calls...)
}

func linkDashboard(t *testing.T) (http.Handler, *linkController) {
	t.Helper()
	ctrl := newLinkController(t)
	return HandlerFromOptionsWithBundle(HandlerOptions{
		Backend:       &fakeBackend{caps: backend.Capabilities{Mode: "cluster"}},
		ControllerURL: ctrl.URL,
		Token:         "service-token",
		RequireLogin:  true,
	}, authTestBundle), ctrl
}

func linkFlow(code string) oauthFlow {
	return oauthFlow{Provider: "github", State: fakeLinkState, Verifier: fakeLinkVerifier, Mode: oauthFlowLink, Code: code}
}

func oauthFlowValue(flow oauthFlow) string {
	raw, _ := json.Marshal(flow)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func linkRequest(csrfForm string) *http.Request {
	form := url.Values{}
	if csrfForm != "" {
		form.Set("csrf_token", csrfForm)
	}
	req := httptest.NewRequest(http.MethodPost, githubAppTestDashHost+"/auth/github/link", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", githubAppTestDashHost)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: githubAppTestSession})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: githubAppTestCSRF})
	return req
}

func linkReturn(handler http.Handler, path string, query url.Values, flow *oauthFlow, session bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, githubAppTestDashHost+path+"?"+query.Encode(), nil)
	if flow != nil {
		req.AddCookie(&http.Cookie{Name: oauthFlowCookie, Value: oauthFlowValue(*flow)})
	}
	if session {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: githubAppTestSession})
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func oauthFlowFrom(t *testing.T, rec *httptest.ResponseRecorder) oauthFlow {
	t.Helper()
	c := findCookie(rec.Result().Cookies(), oauthFlowCookie)
	if c == nil || c.Value == "" {
		t.Fatalf("response set no flow cookie: %v", rec.Result().Cookies())
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		t.Fatal(err)
	}
	var flow oauthFlow
	if err := json.Unmarshal(raw, &flow); err != nil {
		t.Fatal(err)
	}
	return flow
}

func TestIdentityLinkStartsTheFlowAsTheSignedInSession(t *testing.T) {
	t.Parallel()
	handler, ctrl := linkDashboard(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, linkRequest(githubAppTestCSRF))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `content="0;url=`+template.HTMLEscapeString(fakeLinkAuthorizeURL)+`"`) {
		t.Fatalf("link = %d %s, want a page that moves on to the authorize_url", rec.Code, rec.Body)
	}
	cookie := findCookie(rec.Result().Cookies(), oauthFlowCookie)
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 {
		t.Fatalf("flow cookie = %+v, want a short-lived Secure HttpOnly Lax cookie", cookie)
	}
	if flow := oauthFlowFrom(t, rec); flow != linkFlow("") {
		t.Errorf("flow cookie = %+v, want the controller's state and verifier marked as a link", flow)
	}
	calls := ctrl.snapshot()
	if len(calls) != 1 || calls[0].Path != "/api/v1/me/identities/github/link" ||
		calls[0].Authorization != "Session "+githubAppTestSession || calls[0].Body["redirect_uri"] != linkCallbackURI {
		t.Fatalf("controller calls = %+v, want one link start as the user's session with the sign-in callback", calls)
	}
}

func TestIdentityLinkRefusesAFormTheDashboardDidNotServe(t *testing.T) {
	t.Parallel()
	handler, ctrl := linkDashboard(t)
	crossSite := linkRequest(githubAppTestCSRF)
	crossSite.Header.Set("Origin", "https://attacker.example")
	for name, req := range map[string]*http.Request{
		"missing token": linkRequest(""),
		"wrong token":   linkRequest("csrf-someone-else"),
		"cross-site":    crossSite,
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("link with %s = %d, want 403", name, rec.Code)
		}
		if findCookie(rec.Result().Cookies(), oauthFlowCookie) != nil {
			t.Errorf("link with %s set a flow cookie", name)
		}
	}
	if calls := ctrl.snapshot(); len(calls) != 0 {
		t.Fatalf("forged links reached the controller: %+v", calls)
	}
}

// The provider's return carries no session, so a link keeps the code and
// moves on; it never runs the sign-in exchange.
func TestIdentityLinkCallbackKeepsTheCodeAndMovesOnSameSite(t *testing.T) {
	t.Parallel()
	handler, ctrl := linkDashboard(t)
	flow := linkFlow("")
	forged := linkReturn(handler, "/auth/github/callback", url.Values{"code": {"gh-code"}, "state": {"someone-elses"}}, &flow, false)
	if forged.Code != http.StatusBadRequest || findCookie(forged.Result().Cookies(), oauthFlowCookie) != nil {
		t.Fatalf("forged return = %d, cookies %v; want 400 leaving the flow alone", forged.Code, forged.Result().Cookies())
	}
	rec := linkReturn(handler, "/auth/github/callback", url.Values{"code": {"gh-code"}, "state": {fakeLinkState}}, &flow, false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `content="0;url=/auth/github/link/complete"`) {
		t.Fatalf("callback = %d %s, want a page that moves on to completion", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "gh-code") {
		t.Error("callback page carries the code")
	}
	if got := oauthFlowFrom(t, rec); got != linkFlow("gh-code") {
		t.Errorf("flow after callback = %+v, want the code kept", got)
	}
	if calls := ctrl.snapshot(); len(calls) != 0 {
		t.Fatalf("the link callback called the controller: %+v", calls)
	}
}

func TestIdentityLinkCompleteAttachesAsTheSession(t *testing.T) {
	t.Parallel()
	handler, ctrl := linkDashboard(t)
	flow := linkFlow("gh-code")
	rec := linkReturn(handler, "/auth/github/link/complete", nil, &flow, true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account/sign-ins?linked=github" {
		t.Fatalf("complete = %d %q, want 303 to the linked sign-ins page", rec.Code, rec.Header().Get("Location"))
	}
	if c := findCookie(rec.Result().Cookies(), oauthFlowCookie); c == nil || c.MaxAge >= 0 {
		t.Errorf("complete kept the flow cookie: %+v", c)
	}
	calls := ctrl.snapshot()
	if len(calls) != 1 || calls[0].Path != "/api/v1/me/identities/github/link/complete" ||
		calls[0].Authorization != "Session "+githubAppTestSession {
		t.Fatalf("controller calls = %+v, want one complete as the user's session", calls)
	}
	for k, v := range map[string]string{
		"state": fakeLinkState, "verifier": fakeLinkVerifier, "code": "gh-code", "redirect_uri": linkCallbackURI,
	} {
		if calls[0].Body[k] != v {
			t.Errorf("complete body %s = %v, want %v", k, calls[0].Body[k], v)
		}
	}
}

// A refusal reaches the settings page as a code the page words itself, never
// as text from the URL.
func TestIdentityLinkCompleteCarriesTheRefusal(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusConflict, `{"error":"identity_linked_elsewhere","message":"that GitHub sign-in is already linked to another Sparkwing account"}`,
			"/account/sign-ins?provider=github&refused=identity_linked_elsewhere"},
		{http.StatusForbidden, `{"error":"link_state_invalid","message":"this link expired"}`,
			"/account/sign-ins?provider=github&refused=link_state_invalid"},
		{http.StatusConflict, `{"error":"a code this page does not know","message":"<b>hi</b>"}`,
			"/account/sign-ins?provider=github&refused=link_failed"},
		{http.StatusBadGateway, `{"error":"internal server error"}`,
			"/account/sign-ins?provider=github&refused=link_failed"},
	} {
		handler, ctrl := linkDashboard(t)
		ctrl.refuseCompleteWith(test.status, test.body)
		flow := linkFlow("gh-code")
		rec := linkReturn(handler, "/auth/github/link/complete", nil, &flow, true)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != test.want {
			t.Errorf("complete refused with %d %s = %d %q, want 303 to %s",
				test.status, test.body, rec.Code, rec.Header().Get("Location"), test.want)
		}
	}
}

func TestIdentityLinkCompleteRefusesWithoutAFinishedLink(t *testing.T) {
	t.Parallel()
	handler, ctrl := linkDashboard(t)
	signIn := linkFlow("gh-code")
	signIn.Mode = ""
	noCode := linkFlow("")
	for name, flow := range map[string]*oauthFlow{"missing cookie": nil, "a sign-in flow": &signIn, "no code": &noCode} {
		rec := linkReturn(handler, "/auth/github/link/complete", nil, flow, true)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account/sign-ins?provider=github&refused=link_state_invalid" {
			t.Errorf("%s: complete = %d %q, want 303 with link_state_invalid", name, rec.Code, rec.Header().Get("Location"))
		}
	}
	if calls := ctrl.snapshot(); len(calls) != 0 {
		t.Fatalf("unfinished links reached the controller: %+v", calls)
	}
}
