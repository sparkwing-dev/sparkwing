package wingd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

type loginClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *loginClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *loginClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type loginProbe struct {
	mu    sync.Mutex
	peers []localhostwire.LocalPeerIdentity
	calls int
}

func (p *loginProbe) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := localhostwire.LocalPeer(r.Context())
		p.mu.Lock()
		p.calls++
		if ok {
			p.peers = append(p.peers, peer)
		}
		p.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (p *loginProbe) reached() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func noRedirects() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type loginReply struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func loginRequest(t *testing.T, client *http.Client, method, target string, edit func(*http.Request)) loginReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return loginReply{StatusCode: resp.StatusCode, Header: resp.Header, Body: body}
}

func withSession(session string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+session) }
}

func withSessionFrom(session, origin string) func(*http.Request) {
	return func(req *http.Request) {
		withSession(session)(req)
		req.Header.Set("Origin", origin)
	}
}

func signInThrough(t testing.TB, logins *dashboardLogins) string {
	t.Helper()
	uid := uint32(1000)
	login, failure := logins.mint(localhostwire.LocalPeerIdentity{GOOS: "linux", UID: &uid})
	if failure != nil {
		t.Fatal(failure)
	}
	response := exchangeRequest(logins.gate(http.NotFoundHandler(), nil), login.URL, "")
	var grant dashboardSessionGrant
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &grant) != nil || grant.Token == "" {
		t.Fatalf("sign-in exchange: HTTP %d %s", response.Code, response.Body)
	}
	return grant.Token
}

func exchangeRequest(handler http.Handler, link, origin string) *httptest.ResponseRecorder {
	base, code, _ := strings.Cut(link, "/#code=")
	if origin == "" {
		origin = base
	}
	request := httptest.NewRequest(http.MethodPost, origin+"/login", strings.NewReader(`{"code":"`+code+`"}`))
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func servingLogins(clock func() time.Time, origin string) *dashboardLogins {
	logins := newDashboardLogins(clock)
	logins.serving(origin)
	return logins
}

func TestDashboardSessionForwardsUnrelatedCookies(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	request.Header.Set("Authorization", "Bearer stale")
	for _, name := range []string{"sw_session", "__Host-sw_session"} {
		request.AddCookie(&http.Cookie{Name: name, Value: "stale"})
	}
	request.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
	forwarded := false
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		forwarded = true
		cookies := r.Cookies()
		if len(cookies) != 1 || cookies[0].Name != "theme" || cookies[0].Value != "dark" || r.Header.Get("Authorization") != "" {
			t.Fatalf("forwarded cookies %v and authorization %q", cookies, r.Header.Get("Authorization"))
		}
	})
	logins := newDashboardLogins(nil)
	logins.serveSession(httptest.NewRecorder(), request, handler, dashboardSession{digest: "d", expires: time.Now().Add(time.Hour)}, make(chan struct{}))
	if !forwarded {
		t.Fatal("authenticated request did not reach the handler")
	}
	if len(request.Cookies()) != 3 {
		t.Fatal("forwarding changed the original request cookies")
	}
}

func TestLocalDashboardSignsInOnceThroughSocketMintedLink(t *testing.T) {
	clock := &loginClock{now: time.Unix(1_800_000_000, 0)}
	api, dashboard, shell := &loginProbe{}, &loginProbe{}, &loginProbe{}
	f := &runtimeControllerFixture{}
	f.http = func() localhostwire.LocalHTTP {
		return localhostwire.LocalHTTP{API: api.handler(), Dashboard: dashboard.handler(), Shell: shell.handler()}
	}
	r := newRuntimeFixture(t, f)
	r.listen, r.now = nil, clock.Now
	lifecycle := NewLocalLifecycle(r, nil)
	opts := LocalStartOptions{Home: f.home, Version: "v1"}
	if _, err := lifecycle.EnsureStarted(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	socketPath, err := APISocketPath(f.home)
	if err != nil {
		t.Fatal(err)
	}
	socket := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return DialSocket(ctx, socketPath, 5*time.Second)
	}}}
	t.Cleanup(socket.CloseIdleConnections)
	mint := func() DashboardLogin {
		t.Helper()
		resp := loginRequest(t, socket, http.MethodPost, "http://sparkwing-api"+DashboardLoginPath, nil)
		var login DashboardLogin
		if resp.StatusCode != http.StatusOK || json.Unmarshal(resp.Body, &login) != nil {
			t.Fatalf("mint over api.sock: HTTP %d", resp.StatusCode)
		}
		return login
	}
	browser := noRedirects()
	exchange := func(login DashboardLogin, origin string) loginReply {
		t.Helper()
		base, code, _ := strings.Cut(login.URL, "/#code=")
		if origin == "" {
			origin = base
		}
		return loginRequest(t, browser, http.MethodPost, base+"/login", func(req *http.Request) {
			req.Body = io.NopCloser(strings.NewReader(`{"code":"` + code + `"}`))
			req.Header.Set("Content-Type", "application/json")
			if origin != "-" {
				req.Header.Set("Origin", origin)
			}
		})
	}
	signIn := func(login DashboardLogin) string {
		t.Helper()
		resp := exchange(login, "")
		var grant dashboardSessionGrant
		if resp.StatusCode != http.StatusOK || json.Unmarshal(resp.Body, &grant) != nil || len(grant.Token) < 43 {
			t.Fatalf("sign in: HTTP %d %s", resp.StatusCode, resp.Body)
		}
		if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" || len(resp.Header.Values("Set-Cookie")) != 0 {
			t.Fatalf("sign in headers %v", resp.Header)
		}
		if !grant.ExpiresAt.Equal(clock.Now().Add(dashboardSessionLifetime)) {
			t.Fatalf("session expires at %s", grant.ExpiresAt)
		}
		return grant.Token
	}

	if resp := loginRequest(t, socket, http.MethodGet, "http://sparkwing-api/api/v1/runs", nil); resp.StatusCode != http.StatusOK || api.reached() != 1 {
		t.Fatalf("api.sock without a session: HTTP %d", resp.StatusCode)
	}
	login := mint()
	if !login.ExpiresAt.Equal(clock.Now().Add(60 * time.Second)) {
		t.Fatalf("link expires at %s", login.ExpiresAt)
	}
	link, err := url.Parse(login.URL)
	if err != nil || link.Path != "/" || link.RawQuery != "" || !strings.HasPrefix(link.Fragment, "code=") || len(link.Fragment) < 48 {
		t.Fatalf("login link %q", login.URL)
	}
	base := "http://" + link.Host
	runs := base + "/api/v1/runs"

	if resp := loginRequest(t, browser, http.MethodGet, runs, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("TCP API without a session: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, base+DashboardLoginPath, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("TCP mint without a session: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodGet, base+"/runs", nil); resp.StatusCode != http.StatusTeapot || shell.reached() != 1 {
		t.Fatalf("signed-out page: HTTP %d, shell reached %d", resp.StatusCode, shell.reached())
	}
	if dashboard.reached() != 0 {
		t.Fatal("an unauthenticated TCP request reached the dashboard")
	}

	for _, origin := range []string{"-", "null", "http://evil.example"} {
		if resp := exchange(login, origin); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("exchange with Origin %q: HTTP %d", origin, resp.StatusCode)
		}
	}
	if resp := loginRequest(t, browser, http.MethodGet, base+"/login", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET exchange: HTTP %d", resp.StatusCode)
	}
	session := signIn(login)
	if again := exchange(login, ""); again.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second use: HTTP %d", again.StatusCode)
	}
	for _, edit := range []func(*http.Request){
		func(req *http.Request) { req.Header.Set("Cookie", "sw_dashboard="+session) },
		func(req *http.Request) { req.Header.Set("Cookie", "sw_session="+session) },
		func(req *http.Request) { req.Header.Set("Authorization", session) },
		func(req *http.Request) { req.Header.Set("Authorization", "Basic "+session) },
		func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+session+" extra") },
		withSession(session[:len(session)-1] + map[bool]string{true: "B", false: "A"}[strings.HasSuffix(session, "A")]),
	} {
		if resp := loginRequest(t, browser, http.MethodGet, runs, edit); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a presentation other than the session bearer: HTTP %d", resp.StatusCode)
		}
	}
	if resp := loginRequest(t, browser, http.MethodGet, runs, withSession(session)); resp.StatusCode != http.StatusOK {
		t.Fatalf("TCP API with the session: HTTP %d", resp.StatusCode)
	}
	for _, origin := range []string{"", "null"} {
		if resp := loginRequest(t, browser, http.MethodPost, runs, withSessionFrom(session, origin)); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("cookie write with Origin %q: HTTP %d", origin, resp.StatusCode)
		}
	}
	if resp := loginRequest(t, browser, http.MethodPost, runs, withSessionFrom(session, base)); resp.StatusCode != http.StatusOK {
		t.Fatalf("same-origin cookie write: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, base+DashboardLoginPath, withSessionFrom(session, base)); resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") == "application/json" {
		t.Fatal("TCP reached the socket-only mint route")
	}
	dashboard.mu.Lock()
	peers := append([]localhostwire.LocalPeerIdentity(nil), dashboard.peers...)
	dashboard.mu.Unlock()
	api.mu.Lock()
	socketPeer := api.peers[0]
	api.mu.Unlock()
	if len(peers) != 3 || peers[0].GOOS != socketPeer.GOOS || (socketPeer.UID == nil) != (peers[0].UID == nil) || socketPeer.UID != nil && *socketPeer.UID != *peers[0].UID || peers[0].SID != socketPeer.SID {
		t.Fatalf("browser session carries %+v, minting peer was %+v", peers, socketPeer)
	}
	shell.mu.Lock()
	shellPeers := len(shell.peers)
	shell.mu.Unlock()
	if shellPeers != 0 {
		t.Fatal("the signed-out shell saw a peer identity")
	}
	for _, tc := range []struct{ host, origin string }{
		{"rebound.example", ""},
		{link.Host, "http://evil.example"},
		{link.Host, "http://127.0.0.1:1"},
	} {
		resp := loginRequest(t, browser, http.MethodGet, runs, func(req *http.Request) {
			withSession(session)(req)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
		})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("signed-in Host %q Origin %q = %d, want 403", tc.host, tc.origin, resp.StatusCode)
		}
	}

	clock.advance(dashboardSessionLifetime - time.Second)
	if resp := loginRequest(t, browser, http.MethodGet, runs, withSession(session)); resp.StatusCode != http.StatusOK {
		t.Fatalf("session inside 30 days: HTTP %d", resp.StatusCode)
	}
	clock.advance(time.Second)
	if resp := loginRequest(t, browser, http.MethodGet, runs, withSession(session)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session at 30 days: HTTP %d", resp.StatusCode)
	}

	// safety: a restart revokes every sign-in, so a token a port squatter collected while the controller was down
	// is worthless once the controller binds again at the same address.
	session = signIn(mint())
	pending := mint()
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.cfg.Dashboard.Address = link.Host
	if _, err := lifecycle.EnsureStarted(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	status, err := lifecycle.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.DashboardURL != base {
		t.Fatalf("the restarted dashboard moved from %s to %s: %s", base, status.DashboardURL, status.DashboardError)
	}
	if resp := loginRequest(t, browser, http.MethodGet, runs, withSession(session)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a session minted before the restart: HTTP %d", resp.StatusCode)
	}
	if resp := exchange(pending, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a link minted before the restart: HTTP %d", resp.StatusCode)
	}

	session = signIn(mint())
	logout := base + "/logout"
	if resp := loginRequest(t, browser, http.MethodGet, logout, withSession(session)); resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("GET logout: HTTP %d Allow %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
	for _, origin := range []string{"", "http://evil.example"} {
		if resp := loginRequest(t, browser, http.MethodPost, logout, withSessionFrom(session, origin)); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("logout with Origin %q: HTTP %d", origin, resp.StatusCode)
		}
	}
	if resp := loginRequest(t, browser, http.MethodGet, runs, withSession(session)); resp.StatusCode != http.StatusOK {
		t.Fatalf("session after refused logouts: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, logout, withSessionFrom(session, base)); resp.StatusCode != http.StatusNoContent || len(resp.Header.Values("Set-Cookie")) != 0 {
		t.Fatalf("logout: HTTP %d Set-Cookie %q", resp.StatusCode, resp.Header.Values("Set-Cookie"))
	}
	if resp := loginRequest(t, browser, http.MethodGet, runs, withSession(session)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after logout: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, logout, withSessionFrom(session, base)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second logout: HTTP %d", resp.StatusCode)
	}

	stale := mint()
	clock.advance(60 * time.Second)
	if resp := exchange(stale, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired link: HTTP %d", resp.StatusCode)
	}
	fresh := mint()
	clock.advance(59 * time.Second)
	signIn(fresh)
}

func TestLocalDashboardLinkRedeemsOnlyAtItsOrigin(t *testing.T) {
	logins := servingLogins(nil, "http://127.0.0.1:4343")
	uid := uint32(7)
	login, failure := logins.mint(localhostwire.LocalPeerIdentity{GOOS: "linux", UID: &uid})
	if failure != nil {
		t.Fatal(failure)
	}
	gate := logins.gate(http.NotFoundHandler(), nil)
	if response := exchangeRequest(gate, login.URL, "http://127.0.0.1:9999"); response.Code != http.StatusUnauthorized {
		t.Fatalf("redeemed at another port: HTTP %d", response.Code)
	}
	if response := exchangeRequest(gate, login.URL, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("a link refused at another origin stayed redeemable: HTTP %d", response.Code)
	}
	session := signInThrough(t, logins)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9999/api/v1/runs", nil)
	withSession(session)(request)
	response := httptest.NewRecorder()
	logins.gate(http.NotFoundHandler(), nil).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("a session redeemed at one port authorized another: HTTP %d", response.Code)
	}
}

func TestLocalDashboardMintRefusesWithoutServingDashboard(t *testing.T) {
	logins := newDashboardLogins(nil)
	uid := uint32(7)
	if _, failure := logins.mint(localhostwire.LocalPeerIdentity{GOOS: "linux", UID: &uid}); failure == nil || failure.Hint == "" {
		t.Fatalf("mint without a dashboard = %v", failure)
	}
	response := httptest.NewRecorder()
	logins.socketRoutes(http.NotFoundHandler()).ServeHTTP(response, httptest.NewRequest(http.MethodPost, DashboardLoginPath, nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("mint without a verified peer: HTTP %d", response.Code)
	}
}

func FuzzLocalDashboardRequiresSessionBearer(f *testing.F) {
	server := httptest.NewServer(nil)
	f.Cleanup(server.Close)
	logins := servingLogins(nil, server.URL)
	var reached sync.Map
	dashboard := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(r.Header.Get("X-Probe"), true)
		w.WriteHeader(http.StatusOK)
	})
	shell := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := localhostwire.LocalPeer(r.Context()); ok {
			reached.Store(r.Header.Get("X-Probe"), true)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server.Config.Handler = guardDashboard(localhostwire.LocalHTTP{Dashboard: dashboard, Shell: shell}, server.Listener.Addr().String(), logins, false)
	session := signInThrough(f, logins)
	f.Add("GET", "/api/v1/runs", "", "sw_dashboard="+session, "")
	f.Add("POST", "/api/v1/runs/x/cancel", "Bearer "+session[:10], "", "")
	f.Add("GET", "/api/v1/runs", "", "sw_session="+session, "")
	f.Add("POST", "/logout", "", "sw_dashboard="+session, "")
	f.Add("POST", "/login", "", "", "code=")
	f.Add("GET", "/", "Bearer ", "", "")
	f.Add("DELETE", "/login/../api", "bearer "+session, "other=x", "code=")
	client := noRedirects()
	var probe int
	f.Fuzz(func(t *testing.T, method, path, authorization, cookie, query string) {
		if strings.Contains(authorization, "Bearer "+session) {
			t.Skip()
		}
		probe++
		id := strconv.Itoa(probe)
		target := server.URL + "/" + strings.TrimLeft(path, "/")
		if query != "" {
			target += "?" + query
		}
		req, err := http.NewRequestWithContext(t.Context(), method, target, nil)
		if err != nil {
			t.Skip()
		}
		req.Header.Set("X-Probe", id)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Skip()
		}
		_ = resp.Body.Close()
		if _, hit := reached.Load(id); hit {
			t.Fatalf("%s %s with Authorization %q Cookie %q reached the dashboard", method, target, authorization, cookie)
		}
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%s %s with Authorization %q Cookie %q answered 200", method, target, authorization, cookie)
		}
	})
}

func signedInBearer(t *testing.T, r *localRuntime) (string, string) {
	t.Helper()
	r.mu.Lock()
	logins := r.run.logins
	r.mu.Unlock()
	session := signInThrough(t, logins)
	return "Bearer " + session, strings.TrimPrefix(logins.servingOrigin(), "http://")
}

func TestLocalDashboardStreamsEndWithTheirSession(t *testing.T) {
	for _, end := range []string{"logout", "lifetime"} {
		t.Run(end, func(t *testing.T) {
			clock := &loginClock{now: time.Unix(1_800_000_000, 0)}
			entered := make(chan struct{})
			stream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				close(entered)
				<-r.Context().Done()
			})
			server := httptest.NewUnstartedServer(nil)
			logins := servingLogins(clock.Now, "http://"+server.Listener.Addr().String())
			server.Config.Handler = logins.gate(stream, nil)
			server.Start()
			t.Cleanup(server.Close)
			session := signInThrough(t, logins)
			if end == "lifetime" {
				clock.advance(dashboardSessionLifetime - 50*time.Millisecond)
			}
			done := make(chan error, 1)
			go func() {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/runs/r/events/stream", nil)
				if err != nil {
					done <- err
					return
				}
				withSession(session)(req)
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					_, err = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				done <- err
			}()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("stream refused before it opened: %v", err)
			}
			if end == "logout" {
				if resp := loginRequest(t, http.DefaultClient, http.MethodPost, server.URL+"/logout", withSessionFrom(session, server.URL)); resp.StatusCode != http.StatusNoContent {
					t.Fatalf("logout: HTTP %d", resp.StatusCode)
				}
			}
			select {
			case <-done:
			case <-t.Context().Done():
				t.Fatal("an open stream outlived its session")
			}
		})
	}
}

func TestLocalDashboardReadOnlyRequiresAuthAndRefusesChanges(t *testing.T) {
	const origin = "http://192.0.2.1:4343"
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(readOnly), func(t *testing.T) {
			logins := servingLogins(nil, origin)
			session := signInThrough(t, logins)
			handler := guardDashboard(localhostwire.LocalHTTP{Dashboard: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}, "192.0.2.1:4343", logins, readOnly)
			write := map[bool]int{false: http.StatusNoContent, true: http.StatusForbidden}[readOnly]
			for _, tc := range []struct {
				method, session, origin string
				status                  int
			}{
				{"GET", "", "", http.StatusUnauthorized},
				{"POST", "", origin, http.StatusUnauthorized},
				{"GET", session, "", http.StatusNoContent},
				{"GET", session, "http://other.example", http.StatusForbidden},
				{"POST", session, "", http.StatusForbidden},
				{"POST", session, origin, write},
				{"PUT", session, origin, write},
				{"DELETE", session, origin, write},
			} {
				request := httptest.NewRequest(tc.method, origin+"/api/v1/runs", nil)
				if tc.session != "" {
					withSession(tc.session)(request)
				}
				if tc.origin != "" {
					request.Header.Set("Origin", tc.origin)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != tc.status {
					t.Fatalf("%s authenticated=%v origin=%q: status=%d want=%d body=%s", tc.method, tc.session != "", tc.origin, response.Code, tc.status, response.Body)
				}
			}
			request := httptest.NewRequest(http.MethodPost, origin+"/logout", nil)
			withSessionFrom(session, origin)(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("logout: %d %s", response.Code, response.Body)
			}
		})
	}
}

func TestLocalDashboardLogoutAfterLookupStillEndsTheStream(t *testing.T) {
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	logins := servingLogins(nil, origin)
	session := signInThrough(t, logins)
	// safety: Log out lands after this request's lookup succeeded and before it serves, the window the watch must cover.
	logins.afterLookup = func(digest string) {
		logins.revoke(digest)
		logins.end(digest)
	}
	server.Config.Handler = logins.gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		<-r.Context().Done()
	}), nil)
	server.Start()
	t.Cleanup(server.Close)
	// safety: the stream ends on its own when Log out reaches it; only a stream that outlived it reaches this deadline.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/runs/r/events/stream", nil)
		if err != nil {
			return
		}
		withSession(session)(req)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	<-done
	if ctx.Err() != nil {
		t.Fatal("a stream validated just before Log out outlived it")
	}
	logins.mu.Lock()
	watches := len(logins.watch)
	logins.mu.Unlock()
	if watches != 0 {
		t.Fatalf("ended sessions left %d watches", watches)
	}
}

func TestLocalDashboardForgedBearersLeaveNoWatches(t *testing.T) {
	logins := servingLogins(nil, "http://127.0.0.1:4343")
	for i := range 50 {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4343/api/v1/runs", nil)
		withSession(fmt.Sprintf("forged-%d", i))(request)
		logins.gate(http.NotFoundHandler(), nil).ServeHTTP(httptest.NewRecorder(), request)
	}
	if len(logins.watch) != 0 {
		t.Fatalf("forged bearers left %d watches", len(logins.watch))
	}
}

func TestLocalDashboardDefaultPortOrigins(t *testing.T) {
	for _, minted := range []string{"http://127.0.0.1", "http://127.0.0.1:80"} {
		for _, requested := range []string{"http://127.0.0.1", "http://127.0.0.1:80"} {
			t.Run(minted+"/"+requested, func(t *testing.T) {
				logins := servingLogins(nil, minted)
				login, failure := logins.mint(localhostwire.LocalPeerIdentity{})
				if failure != nil {
					t.Fatal(failure)
				}
				response := exchangeRequest(logins.gate(http.NotFoundHandler(), nil), login.URL, requested)
				var grant dashboardSessionGrant
				if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &grant) != nil {
					t.Fatalf("exchange: %d %s", response.Code, response.Body)
				}
				request := httptest.NewRequest(http.MethodPost, minted+"/logout", nil)
				withSessionFrom(grant.Token, requested)(request)
				response = httptest.NewRecorder()
				logins.gate(http.NotFoundHandler(), nil).ServeHTTP(response, request)
				if response.Code != http.StatusNoContent {
					t.Fatalf("logout: %d %s", response.Code, response.Body)
				}
			})
		}
	}
}

func TestLocalDashboardBoundsAuthBodies(t *testing.T) {
	logins := servingLogins(nil, "http://127.0.0.1:4343")
	for _, path := range []string{"/login", "/logout"} {
		request := httptest.NewRequest(http.MethodPost, logins.servingOrigin()+path, strings.NewReader(strings.Repeat(" ", 4097)))
		withSessionFrom(signInThrough(t, logins), logins.servingOrigin())(request)
		response := httptest.NewRecorder()
		logins.gate(http.NotFoundHandler(), nil).ServeHTTP(response, request)
		if response.Code < 400 {
			t.Fatalf("%s accepted oversized body: %d", path, response.Code)
		}
	}
}

func TestLocalDashboardSlowAuthDoesNotBlockOtherSessions(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	logins := servingLogins(nil, origin)
	reading := make(chan struct{}, 2)
	gate := logins.gate(http.NotFoundHandler(), nil)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reading <- struct{}{}
		gate.ServeHTTP(w, r)
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveLocalHTTP(ctx, listener, handler, nil, nil, 200*time.Millisecond) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for _, path := range []string{"/login", "/logout"} {
		connection, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(connection, "POST %s HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nContent-Length: 100\r\n\r\n{", path, listener.Addr(), origin)
		if err != nil {
			t.Fatal(err)
		}
		<-reading
		session := signInThrough(t, logins)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, origin+"/logout", nil)
		withSessionFrom(session, origin)(request)
		gate.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("concurrent logout: %d", response.Code)
		}
		responseBytes, err := io.ReadAll(connection)
		connection.Close()
		if err != nil {
			t.Fatalf("slow %s was not disconnected: %v", path, err)
		}
		if !strings.Contains(string(responseBytes), "400") && !strings.Contains(string(responseBytes), "401") {
			t.Fatalf("slow %s response: %s", path, responseBytes)
		}
	}
}
