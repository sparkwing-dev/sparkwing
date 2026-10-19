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
	cookies    []*http.Cookie
	Body       []byte
}

func (r loginReply) Cookies() []*http.Cookie { return r.cookies }

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
	return loginReply{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies(), Body: body}
}

func TestLocalDashboardSignsInOnceThroughSocketMintedLink(t *testing.T) {
	clock := &loginClock{now: time.Unix(1_800_000_000, 0)}
	api, dashboard := &loginProbe{}, &loginProbe{}
	f := &runtimeControllerFixture{}
	f.http = func() LocalHTTP { return LocalHTTP{API: api.handler(), Dashboard: dashboard.handler()} }
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

	if resp := loginRequest(t, socket, http.MethodGet, "http://sparkwing-api/api/v1/runs", nil); resp.StatusCode != http.StatusOK || api.reached() != 1 {
		t.Fatalf("api.sock without a cookie: HTTP %d", resp.StatusCode)
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
	browser := noRedirects()

	if resp := loginRequest(t, browser, http.MethodGet, base+"/api/v1/runs", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("TCP API without a cookie: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, base+DashboardLoginPath, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("TCP mint without a cookie: HTTP %d", resp.StatusCode)
	}
	if dashboard.reached() != 0 {
		t.Fatal("an unauthenticated TCP request reached the dashboard")
	}

	resp := loginRequest(t, browser, http.MethodGet, login.URL, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("first use: HTTP %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	raw := resp.Header.Values("Set-Cookie")
	if len(raw) != 1 {
		t.Fatalf("Set-Cookie = %q", raw)
	}
	lower := strings.ToLower(raw[0])
	for _, want := range []string{"sw_local_session=", "; path=/", "; httponly", "; samesite=strict"} {
		if !strings.Contains(lower, want) {
			t.Errorf("cookie %q lacks %q", raw[0], want)
		}
	}
	if strings.Contains(lower, "domain=") {
		t.Errorf("cookie %q is not host-only", raw[0])
	}
	cookie := resp.Cookies()[0]
	if again := loginRequest(t, browser, http.MethodGet, login.URL, nil); again.StatusCode != http.StatusUnauthorized || len(again.Cookies()) != 0 {
		t.Fatalf("second use: HTTP %d", again.StatusCode)
	}

	withCookie := func(c *http.Cookie) func(*http.Request) {
		return func(req *http.Request) { req.AddCookie(c) }
	}
	if resp := loginRequest(t, browser, http.MethodGet, base+"/api/v1/runs", withCookie(cookie)); resp.StatusCode != http.StatusOK {
		t.Fatalf("TCP API with the cookie: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, base+DashboardLoginPath, withCookie(cookie)); resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") == "application/json" {
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
	forged := &http.Cookie{Name: cookie.Name, Value: cookie.Value[:len(cookie.Value)-1] + "A"}
	if forged.Value == cookie.Value {
		forged.Value = cookie.Value[:len(cookie.Value)-1] + "B"
	}
	if resp := loginRequest(t, browser, http.MethodGet, base+"/api/v1/runs", withCookie(forged)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged cookie: HTTP %d", resp.StatusCode)
	}

	for _, tc := range []struct{ host, origin string }{
		{"rebound.example", ""},
		{link.Host, "http://evil.example"},
		{link.Host, "http://127.0.0.1:1"},
	} {
		resp := loginRequest(t, browser, http.MethodGet, base+"/api/v1/runs", func(req *http.Request) {
			req.AddCookie(cookie)
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
	if resp := loginRequest(t, browser, http.MethodGet, base+"/api/v1/runs", withCookie(cookie)); resp.StatusCode != http.StatusOK {
		t.Fatalf("session used inside its idle window: HTTP %d", resp.StatusCode)
	}
	clock.advance(12*time.Hour - time.Second)
	if resp := loginRequest(t, browser, http.MethodGet, base+"/api/v1/runs", withCookie(cookie)); resp.StatusCode != http.StatusOK {
		t.Fatalf("use did not renew the idle window: HTTP %d", resp.StatusCode)
	}
	clock.advance(12 * time.Hour)
	if resp := loginRequest(t, browser, http.MethodGet, base+"/api/v1/runs", withCookie(cookie)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session idle for 12 hours: HTTP %d", resp.StatusCode)
	}

	stale := mint()
	clock.advance(60 * time.Second)
	if resp := loginRequest(t, browser, http.MethodGet, stale.URL, nil); resp.StatusCode != http.StatusUnauthorized || len(resp.Cookies()) != 0 {
		t.Fatalf("expired link: HTTP %d", resp.StatusCode)
	}
	fresh := mint()
	clock.advance(59 * time.Second)
	if resp := loginRequest(t, browser, http.MethodGet, fresh.URL, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("link inside its lifetime: HTTP %d", resp.StatusCode)
	}
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

func FuzzLocalDashboardRequiresSessionCookie(f *testing.F) {
	clock := &loginClock{now: time.Unix(1_800_000_000, 0)}
	logins := newDashboardLogins(clock.Now)
	var reached sync.Map
	server := httptest.NewServer(nil)
	f.Cleanup(server.Close)
	server.Config.Handler = guardDashboard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(r.Header.Get("X-Probe"), true)
		w.WriteHeader(http.StatusOK)
	}), server.Listener.Addr().String(), logins)
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
	f.Add("GET", "/api/v1/runs", "", "")
	f.Add("POST", "/api/v1/runs/x/cancel", "sw_local_session="+session[:10], "")
	f.Add("GET", "/login", "", "token="+token)
	f.Add("GET", "/", "sw_local_session=", "")
	f.Add("DELETE", "/login/../api", "other=x; sw_local_session=", "token=")
	client := noRedirects()
	var probe int
	f.Fuzz(func(t *testing.T, method, path, cookie, query string) {
		if cookie == "sw_local_session="+session || strings.Contains(cookie, session) {
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
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Skip()
		}
		_ = resp.Body.Close()
		if _, hit := reached.Load(id); hit {
			t.Fatalf("%s %s with Cookie %q reached the handler", method, target, cookie)
		}
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%s %s with Cookie %q answered 200", method, target, cookie)
		}
	})
}

func signedInCookie(t *testing.T, r *localRuntime) string {
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
	return dashboardSessionCookie + "=" + session
}
