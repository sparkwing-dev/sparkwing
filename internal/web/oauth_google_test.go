package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

const (
	fakeAuthorizeURL = "https://accounts.google.example/o/oauth2/v2/auth?client_id=demo"
	fakeOAuthState   = "state-from-controller"
	fakeVerifier     = "verifier-from-controller"
)

type identityController struct {
	*httptest.Server

	mu         sync.Mutex
	teams      bool
	starts     []map[string]string
	exchanges  []map[string]string
	upstream   []string
	upstreamAZ []string
}

func newIdentityController(t *testing.T, teams bool) *identityController {
	t.Helper()
	c := &identityController{teams: teams}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.Close)
	return c
}

func (c *identityController) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	decode := func() map[string]string {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		return body
	}
	switch r.URL.Path {
	case "/api/v1/capabilities":
		if !c.teams {
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"teams": map[string]bool{"enabled": true},
			"auth":  map[string][]string{"providers": {"google"}},
		})
	case "/api/v1/auth/bootstrap-needed":
		_ = json.NewEncoder(w).Encode(map[string]bool{"needed": false})
	case "/api/v1/auth/oauth/google/start":
		c.starts = append(c.starts, decode())
		_ = json.NewEncoder(w).Encode(oauthStartResp{
			AuthorizeURL: fakeAuthorizeURL, State: fakeOAuthState, Verifier: fakeVerifier,
		})
	case "/api/v1/auth/oauth/google/exchange":
		c.exchanges = append(c.exchanges, decode())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id":  "google-session",
			"user":        map[string]string{"id": "u1", "email": "ada@example.com", "name": "Ada"},
			"active_team": map[string]string{"slug": "ada", "display_name": "Ada's space", "role": "owner"},
		})
	case "/api/v1/auth/session":
		_ = json.NewEncoder(w).Encode(sessionResp{
			Principal: "user:ada@example.com",
			Scopes:    []string{"runs.read"},
			CSRFToken: "csrf-" + strings.TrimPrefix(r.Header.Get("Authorization"), "Session "),
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		})
	default:
		c.upstream = append(c.upstream, r.Method+" "+r.URL.Path)
		c.upstreamAZ = append(c.upstreamAZ, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}
}

func (c *identityController) snapshot() (starts, exchanges []map[string]string, upstream, authz []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]string{}, c.starts...), append([]map[string]string{}, c.exchanges...),
		append([]string{}, c.upstream...), append([]string{}, c.upstreamAZ...)
}

func teamDashboard(t *testing.T, controllerURL string) http.Handler {
	t.Helper()
	return HandlerFromOptionsWithBundle(HandlerOptions{
		Backend:       &fakeBackend{caps: backend.Capabilities{Mode: "cluster"}},
		ControllerURL: controllerURL,
		Token:         "service-token",
		RequireLogin:  true,
	}, authTestBundle)
}

func flowCookieValue(state, verifier, next string) string {
	raw, _ := json.Marshal(oauthFlow{State: state, Verifier: verifier, Next: next})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestGoogleStartBindsTheFlowToThisBrowser(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	handler := teamDashboard(t, ctrl.URL)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"https://dashboard.example/auth/google/start?next=%2Fruns%3Fx%3D1", nil))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fakeAuthorizeURL {
		t.Fatalf("start = %d %q, want 303 to the controller's authorize_url", rec.Code, rec.Header().Get("Location"))
	}
	flow := findCookie(rec.Result().Cookies(), "__Host-sw_oauth")
	if flow == nil {
		t.Fatalf("start set no __Host-sw_oauth cookie: %v", rec.Result().Cookies())
	}
	if !flow.Secure || !flow.HttpOnly || flow.SameSite != http.SameSiteLaxMode || flow.Path != "/" || flow.Domain != "" {
		t.Errorf("flow cookie attributes = Secure %v HttpOnly %v SameSite %v Path %q Domain %q; want Secure HttpOnly Lax / host-only",
			flow.Secure, flow.HttpOnly, flow.SameSite, flow.Path, flow.Domain)
	}
	if flow.MaxAge <= 0 || flow.MaxAge > int(oauthFlowTTL/time.Second) {
		t.Errorf("flow cookie MaxAge = %d, want short-lived (<= %d)", flow.MaxAge, int(oauthFlowTTL/time.Second))
	}
	if flow.Value != flowCookieValue(fakeOAuthState, fakeVerifier, "/runs?x=1") {
		t.Errorf("flow cookie does not carry the controller's state, verifier and next")
	}
	starts, _, _, _ := ctrl.snapshot()
	if len(starts) != 1 || starts[0]["redirect_uri"] != "https://dashboard.example/auth/google/callback" {
		t.Errorf("controller start bodies = %v, want the dashboard callback as redirect_uri", starts)
	}
}

func TestGoogleStartOnLocalhostUsesThePlainHTTPCallback(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	handler := teamDashboard(t, ctrl.URL)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost:4343/auth/google/start", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303", rec.Code)
	}
	if flow := findCookie(rec.Result().Cookies(), "__Host-sw_oauth"); flow == nil || !flow.Secure {
		t.Fatalf("localhost flow cookie = %+v, want the Secure __Host- cookie a browser keeps on localhost", flow)
	}
	starts, _, _, _ := ctrl.snapshot()
	if len(starts) != 1 || starts[0]["redirect_uri"] != "http://localhost:4343/auth/google/callback" {
		t.Errorf("controller start bodies = %v, want http://localhost:4343/auth/google/callback", starts)
	}
}

func googleCallback(handler http.Handler, query, flowCookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "https://dashboard.example/auth/google/callback?"+query, nil)
	if flowCookie != "" {
		req.AddCookie(&http.Cookie{Name: "__Host-sw_oauth", Value: flowCookie})
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestGoogleCallbackRefusesAMissingFlowCookie(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	rec := googleCallback(teamDashboard(t, ctrl.URL), "code=attacker-code&state="+fakeOAuthState, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback without flow cookie = %d, want 400", rec.Code)
	}
	if findCookie(rec.Result().Cookies(), sessionCookieName) != nil {
		t.Fatal("callback without flow cookie set a session cookie")
	}
	if _, exchanges, _, _ := ctrl.snapshot(); len(exchanges) != 0 {
		t.Fatalf("callback without flow cookie reached the controller exchange: %v", exchanges)
	}
}

func TestGoogleCallbackRefusesAStateThisBrowserDidNotStart(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	cookie := flowCookieValue(fakeOAuthState, fakeVerifier, "/")
	for _, query := range []string{
		"code=attacker-code&state=attacker-state",
		"code=attacker-code&state=",
		"code=attacker-code",
		"code=attacker-code&state=" + fakeOAuthState + "x",
	} {
		rec := googleCallback(teamDashboard(t, ctrl.URL), query, cookie)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("callback %q = %d, want 400", query, rec.Code)
		}
		if findCookie(rec.Result().Cookies(), sessionCookieName) != nil {
			t.Fatalf("callback %q set a session cookie", query)
		}
		if c := findCookie(rec.Result().Cookies(), "__Host-sw_oauth"); c != nil {
			t.Fatalf("callback %q discarded the flow cookie this browser is midway through", query)
		}
	}
	if _, exchanges, _, _ := ctrl.snapshot(); len(exchanges) != 0 {
		t.Fatalf("forged callbacks reached the controller exchange: %v", exchanges)
	}
}

func TestGoogleCallbackExchangesAndSignsIn(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	rec := googleCallback(teamDashboard(t, ctrl.URL),
		"code=real-code&state="+fakeOAuthState, flowCookieValue(fakeOAuthState, fakeVerifier, "/crons"))
	if rec.Code != http.StatusOK {
		t.Fatalf("callback = %d, want 200 interstitial: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `content="0;url=/crons"`) {
		t.Errorf("interstitial does not move on to next: %s", rec.Body)
	}
	cookies := rec.Result().Cookies()
	sess := findCookie(cookies, sessionCookieName)
	if sess == nil || sess.Value != "google-session" || !sess.HttpOnly || !sess.Secure {
		t.Fatalf("session cookie = %+v, want the exchanged session, HttpOnly and Secure", sess)
	}
	if csrf := findCookie(cookies, csrfCookieName); csrf == nil || csrf.Value != "csrf-google-session" {
		t.Errorf("csrf cookie = %+v, want the session's own token", csrf)
	}
	if flow := findCookie(cookies, "__Host-sw_oauth"); flow == nil || flow.MaxAge >= 0 {
		t.Errorf("flow cookie = %+v, want it spent", flow)
	}
	_, exchanges, _, _ := ctrl.snapshot()
	want := map[string]string{
		"code": "real-code", "verifier": fakeVerifier,
		"redirect_uri": "https://dashboard.example/auth/google/callback",
	}
	if len(exchanges) != 1 {
		t.Fatalf("exchanges = %v, want one", exchanges)
	}
	for k, v := range want {
		if exchanges[0][k] != v {
			t.Errorf("exchange %s = %q, want %q", k, exchanges[0][k], v)
		}
	}
}

func TestGoogleCallbackReportsAProviderRefusal(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	rec := googleCallback(teamDashboard(t, ctrl.URL),
		"error=access_denied&state="+fakeOAuthState, flowCookieValue(fakeOAuthState, fakeVerifier, "/"))
	if rec.Code != http.StatusUnauthorized || findCookie(rec.Result().Cookies(), sessionCookieName) != nil {
		t.Fatalf("provider refusal = %d, want 401 and no session", rec.Code)
	}
}

func TestLoginPageOffersGoogleOnlyWhenTheControllerDoes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		teams bool
	}{{"multi-team controller", true}, {"single-team controller", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := newIdentityController(t, tc.teams)
			rec := httptest.NewRecorder()
			teamDashboard(t, ctrl.URL).ServeHTTP(rec,
				httptest.NewRequest(http.MethodGet, "https://dashboard.example/login?next=%2Fruns", nil))
			offered := strings.Contains(rec.Body.String(), "Sign in with Google")
			if offered != tc.teams {
				t.Fatalf("Google button shown = %v, want %v", offered, tc.teams)
			}
			if tc.teams && !strings.Contains(rec.Body.String(), `href="/auth/google/start?next=%2fruns"`) {
				t.Errorf("Google button does not carry next: %s", rec.Body)
			}
		})
	}
}

func signedInRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, "https://dashboard.example"+path, strings.NewReader(body))
	req.Header.Set("Origin", "https://dashboard.example")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeaderName, "csrf-user-session")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "user-session"})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf-user-session"})
	return req
}

func TestProxyForwardsTheUsersOwnSessionNotTheServiceToken(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	handler := teamDashboard(t, ctrl.URL)
	for _, req := range []*http.Request{
		signedInRequest(http.MethodGet, "/api/v1/me", ""),
		signedInRequest(http.MethodPost, "/api/v1/me/active-team", `{"slug":"acme"}`),
		signedInRequest(http.MethodGet, "/api/v1/runs", ""),
	} {
		req.Header.Set("Authorization", "Bearer browser-supplied")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s %s = %d, want 204: %s", req.Method, req.URL.Path, rec.Code, rec.Body)
		}
	}
	_, _, upstream, authz := ctrl.snapshot()
	if len(upstream) != 3 {
		t.Fatalf("upstream = %v, want three forwarded requests", upstream)
	}
	for i, got := range authz {
		if got != "Session user-session" {
			t.Errorf("%s reached the controller as %q, want the user's own session", upstream[i], got)
		}
	}
}

func TestTeamMutationsKeepSessionBoundCSRF(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	handler := teamDashboard(t, ctrl.URL)
	for _, path := range []string{
		"/api/v1/me/active-team", "/api/v1/teams", "/api/v1/team/invitations",
		"/api/v1/team/runner-tokens", "/api/v1/invitations/inv1/accept",
	} {
		missing := signedInRequest(http.MethodPost, path, `{}`)
		missing.Header.Del(csrfHeaderName)
		crossSite := signedInRequest(http.MethodPost, path, `{}`)
		crossSite.Header.Set("Origin", "https://attacker.example")
		for name, req := range map[string]*http.Request{"missing header": missing, "cross-site": crossSite} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST %s with %s = %d, want 403", path, name, rec.Code)
			}
		}
	}
	if _, _, upstream, _ := ctrl.snapshot(); len(upstream) != 0 {
		t.Fatalf("forged team mutations reached the controller: %v", upstream)
	}
}

func TestBackendReadsCarryTheUsersSession(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []string
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "r1"})
	}))
	t.Cleanup(ctrl.Close)
	c := client.NewWithToken(ctrl.URL, &http.Client{Transport: SessionForwardingTransport(nil)}, "service-token")

	signedIn := contextWithWebPrincipal(context.Background(), &sessionResp{Principal: "ada"}, "user-session")
	if _, err := c.GetRun(signedIn, "r1"); err != nil {
		t.Fatalf("GetRun as a signed-in user: %v", err)
	}
	if _, err := c.GetRun(context.Background(), "r1"); err != nil {
		t.Fatalf("GetRun with no session: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "Session user-session" || got[1] != "Bearer service-token" {
		t.Fatalf("controller saw %v, want the user's session, then the service token for a sessionless call", got)
	}
}

func TestCapabilitiesCarryTheControllersTeamsAndProviders(t *testing.T) {
	t.Parallel()
	ctrl := newIdentityController(t, true)
	rec := httptest.NewRecorder()
	teamDashboard(t, ctrl.URL).ServeHTTP(rec, signedInRequest(http.MethodGet, "/api/v1/capabilities", ""))
	var caps backend.Capabilities
	if err := json.NewDecoder(rec.Body).Decode(&caps); err != nil {
		t.Fatal(err)
	}
	if caps.Teams == nil || !caps.Teams.Enabled || caps.Auth == nil || len(caps.Auth.Providers) != 1 || caps.Auth.Providers[0] != "google" {
		t.Fatalf("capabilities = %+v, want teams enabled and google offered", caps)
	}

	local := httptest.NewRecorder()
	HandlerFromOptionsWithBundle(HandlerOptions{
		Backend: &fakeBackend{caps: backend.Capabilities{Mode: "local"}},
	}, authTestBundle).ServeHTTP(local, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4343/api/v1/capabilities", nil))
	var raw map[string]any
	if err := json.NewDecoder(local.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"teams", "auth"} {
		if _, ok := raw[key]; ok {
			t.Errorf("local capabilities carry %q; a local install must read as before", key)
		}
	}
}
