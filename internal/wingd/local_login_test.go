package wingd

import (
	"context"
	"encoding/json"
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
	peers []LocalPeerIdentity
	calls int
}

func (p *loginProbe) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := LocalPeer(r.Context())
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

func bearer(session string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+session) }
}

func TestLocalDashboardSignsInOnceThroughSocketMintedLink(t *testing.T) {
	clock := &loginClock{now: time.Unix(1_800_000_000, 0)}
	api, dashboard, shell := &loginProbe{}, &loginProbe{}, &loginProbe{}
	f := &runtimeControllerFixture{}
	f.http = func() LocalHTTP {
		return LocalHTTP{API: api.handler(), Dashboard: dashboard.handler(), Shell: shell.handler()}
	}
	r := newRuntimeFixture(t, f)
	r.listen, r.now = nil, clock.Now
	lifecycle := NewLocalLifecycle(r, nil)
	opts := LocalStartOptions{Home: f.home, Version: "v1", Stay: true, DashboardAddress: "127.0.0.1:0"}
	if _, err := lifecycle.StartDashboard(t.Context(), opts); err != nil {
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
	signIn := func(login DashboardLogin) string {
		t.Helper()
		resp := loginRequest(t, browser, http.MethodGet, login.URL, nil)
		session, found := strings.CutPrefix(resp.Header.Get("Location"), "/#s=")
		if resp.StatusCode != http.StatusSeeOther || !found || len(session) < 43 {
			t.Fatalf("sign in: HTTP %d to %q", resp.StatusCode, resp.Header.Get("Location"))
		}
		if cookies := resp.Header.Values("Set-Cookie"); len(cookies) != 0 {
			t.Fatalf("sign in set cookies %q", cookies)
		}
		if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("sign in headers %v", resp.Header)
		}
		return session
	}

	if resp := loginRequest(t, socket, http.MethodGet, "http://sparkwing-api/api/v1/runs", nil); resp.StatusCode != http.StatusOK || api.reached() != 1 {
		t.Fatalf("api.sock without a bearer: HTTP %d", resp.StatusCode)
	}
	login := mint()
	if !login.ExpiresAt.Equal(clock.Now().Add(60 * time.Second)) {
		t.Fatalf("link expires at %s", login.ExpiresAt)
	}
	link, err := url.Parse(login.URL)
	if err != nil || link.Path != "/login" || len(link.Query().Get("token")) < 43 {
		t.Fatalf("login link %q", login.URL)
	}
	base := "http://" + link.Host
	runs := base + "/api/v1/runs"

	if resp := loginRequest(t, browser, http.MethodGet, runs, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("TCP API without a bearer: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, base+DashboardLoginPath, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("TCP mint without a bearer: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, base+"/", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("TCP page write without a bearer: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodGet, base+"/runs", nil); resp.StatusCode != http.StatusTeapot || shell.reached() != 1 {
		t.Fatalf("signed-out page: HTTP %d, shell reached %d", resp.StatusCode, shell.reached())
	}
	if dashboard.reached() != 0 {
		t.Fatal("an unauthenticated TCP request reached the dashboard")
	}

	session := signIn(login)
	if again := loginRequest(t, browser, http.MethodGet, login.URL, nil); again.StatusCode != http.StatusUnauthorized || len(again.Header.Values("Set-Cookie")) != 0 {
		t.Fatalf("second use: HTTP %d", again.StatusCode)
	}

	for _, cookie := range []string{"sw_local_session=" + session, "session=" + session, "Authorization=Bearer " + session} {
		if resp := loginRequest(t, browser, http.MethodGet, runs, func(req *http.Request) { req.Header.Set("Cookie", cookie) }); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("cookie %q alone: HTTP %d", cookie, resp.StatusCode)
		}
	}
	if resp := loginRequest(t, browser, http.MethodGet, runs, bearer(session)); resp.StatusCode != http.StatusOK {
		t.Fatalf("TCP API with the bearer: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, base+DashboardLoginPath, bearer(session)); resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") == "application/json" {
		t.Fatal("TCP reached the socket-only mint route")
	}
	dashboard.mu.Lock()
	peers := append([]LocalPeerIdentity(nil), dashboard.peers...)
	dashboard.mu.Unlock()
	api.mu.Lock()
	socketPeer := api.peers[0]
	api.mu.Unlock()
	if len(peers) != 2 || peers[0].GOOS != socketPeer.GOOS || (socketPeer.UID == nil) != (peers[0].UID == nil) || socketPeer.UID != nil && *socketPeer.UID != *peers[0].UID || peers[0].SID != socketPeer.SID {
		t.Fatalf("browser session carries %+v, minting peer was %+v", peers, socketPeer)
	}
	shell.mu.Lock()
	shellPeers := len(shell.peers)
	shell.mu.Unlock()
	if shellPeers != 0 {
		t.Fatal("the signed-out shell saw a peer identity")
	}
	forged := session[:len(session)-1] + "A"
	if forged == session {
		forged = session[:len(session)-1] + "B"
	}
	for _, edit := range []func(*http.Request){bearer(forged), func(req *http.Request) { req.Header.Set("Authorization", session) }, func(req *http.Request) { req.Header.Set("Authorization", "Basic "+session) }} {
		if resp := loginRequest(t, browser, http.MethodGet, runs, edit); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("malformed bearer: HTTP %d", resp.StatusCode)
		}
	}

	for _, tc := range []struct{ host, origin string }{
		{"rebound.example", ""},
		{link.Host, "http://evil.example"},
		{link.Host, "http://127.0.0.1:1"},
	} {
		resp := loginRequest(t, browser, http.MethodGet, runs, func(req *http.Request) {
			bearer(session)(req)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
		})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("signed-in Host %q Origin %q = %d, want 403", tc.host, tc.origin, resp.StatusCode)
		}
	}

	clock.advance(12*time.Hour - time.Second)
	if resp := loginRequest(t, browser, http.MethodGet, runs, bearer(session)); resp.StatusCode != http.StatusOK {
		t.Fatalf("session used inside its idle window: HTTP %d", resp.StatusCode)
	}
	clock.advance(12 * time.Hour)
	if resp := loginRequest(t, browser, http.MethodGet, runs, bearer(session)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session idle for 12 hours: HTTP %d", resp.StatusCode)
	}

	session = signIn(mint())
	for _, step := range []time.Duration{6 * time.Hour, 6 * time.Hour, 6 * time.Hour, 6*time.Hour - time.Second} {
		clock.advance(step)
		if resp := loginRequest(t, browser, http.MethodGet, runs, bearer(session)); resp.StatusCode != http.StatusOK {
			t.Fatalf("active session before 24 hours: HTTP %d", resp.StatusCode)
		}
	}
	clock.advance(time.Second)
	if resp := loginRequest(t, browser, http.MethodGet, runs, bearer(session)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("active session at 24 hours: HTTP %d", resp.StatusCode)
	}

	session = signIn(mint())
	logout := base + "/logout"
	if resp := loginRequest(t, browser, http.MethodGet, logout, bearer(session)); resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("GET logout: HTTP %d Allow %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
	if resp := loginRequest(t, browser, http.MethodPost, logout, func(req *http.Request) { req.Header.Set("Cookie", "sw_local_session="+session) }); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logout without a bearer: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodGet, runs, bearer(session)); resp.StatusCode != http.StatusOK {
		t.Fatalf("session after refused logouts: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, logout, func(req *http.Request) {
		bearer(session)(req)
		req.Header.Set("Origin", "http://evil.example")
	}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin logout: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, logout, bearer(session)); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodGet, runs, bearer(session)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after logout: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, logout, bearer(session)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second logout: HTTP %d", resp.StatusCode)
	}

	stale := mint()
	clock.advance(60 * time.Second)
	if resp := loginRequest(t, browser, http.MethodGet, stale.URL, nil); resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("Location") != "" {
		t.Fatalf("expired link: HTTP %d", resp.StatusCode)
	}
	fresh := mint()
	clock.advance(59 * time.Second)
	signIn(fresh)
}

func TestLocalDashboardMintRefusesWithoutServingDashboard(t *testing.T) {
	logins := newDashboardLogins(nil)
	uid := uint32(7)
	if _, failure := logins.mint(LocalPeerIdentity{GOOS: "linux", UID: &uid}); failure == nil || failure.Hint == "" {
		t.Fatalf("mint without a dashboard = %v", failure)
	}
	response := httptest.NewRecorder()
	logins.socketRoutes(http.NotFoundHandler()).ServeHTTP(response, httptest.NewRequest(http.MethodPost, DashboardLoginPath, nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("mint without a verified peer: HTTP %d", response.Code)
	}
}

func FuzzLocalDashboardRequiresBearer(f *testing.F) {
	clock := &loginClock{now: time.Unix(1_800_000_000, 0)}
	logins := newDashboardLogins(clock.Now)
	var reached sync.Map
	server := httptest.NewServer(nil)
	f.Cleanup(server.Close)
	dashboard := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(r.Header.Get("X-Probe"), true)
		w.WriteHeader(http.StatusOK)
	})
	shell := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := LocalPeer(r.Context()); ok {
			reached.Store(r.Header.Get("X-Probe"), true)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server.Config.Handler = guardDashboard(LocalHTTP{Dashboard: dashboard, Shell: shell}, server.Listener.Addr().String(), logins)
	logins.serving(server.URL)
	uid := uint32(1000)
	login, failure := logins.mint(LocalPeerIdentity{GOOS: "linux", UID: &uid})
	if failure != nil {
		f.Fatal(failure)
	}
	token := strings.TrimPrefix(login.URL, server.URL+"/login?token=")
	session, ok := logins.redeem(token)
	if !ok {
		f.Fatal("seed session")
	}
	f.Add("GET", "/api/v1/runs", "", "", "")
	f.Add("POST", "/api/v1/runs/x/cancel", "Bearer "+session[:10], "", "")
	f.Add("GET", "/api/v1/runs", "", "sw_local_session="+session, "")
	f.Add("POST", "/logout", "", "sw_local_session="+session, "")
	f.Add("GET", "/login", "", "", "token="+token)
	f.Add("GET", "/", "Bearer ", "", "")
	f.Add("DELETE", "/login/../api", "bearer x", "other=x; sw_local_session=", "token=")
	client := noRedirects()
	var probe int
	f.Fuzz(func(t *testing.T, method, path, authorization, cookie, query string) {
		if strings.Contains(authorization, session) {
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
	if _, live := logins.session(&http.Request{Header: http.Header{"Authorization": {"Bearer " + session}}}); !live {
		f.Fatal("a request without the bearer ended the session")
	}
}

func signedInBearer(t *testing.T, r *localRuntime) string {
	t.Helper()
	r.mu.Lock()
	logins := r.run.logins
	r.mu.Unlock()
	uid := uint32(1000)
	login, failure := logins.mint(LocalPeerIdentity{GOOS: "linux", UID: &uid})
	if failure != nil {
		t.Fatal(failure)
	}
	link, err := url.Parse(login.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, ok := logins.redeem(link.Query().Get("token"))
	if !ok {
		t.Fatal("fresh sign-in link was refused")
	}
	return "Bearer " + session
}
