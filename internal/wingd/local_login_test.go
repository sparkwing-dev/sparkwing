package wingd

import (
	"context"
	"encoding/json"
	"errors"
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

type memoryDashboardSessions struct {
	mu   sync.Mutex
	rows map[string]memoryDashboardRow
}

type memoryDashboardRow struct {
	code    bool
	origin  string
	peer    localhostwire.LocalPeerIdentity
	expires time.Time
}

func (m *memoryDashboardSessions) put(digest string, row memoryDashboardRow) {
	if m.rows == nil {
		m.rows = map[string]memoryDashboardRow{}
	}
	m.rows[digest] = row
}

func (m *memoryDashboardSessions) MintDashboardCode(_ context.Context, digest, origin string, peer localhostwire.LocalPeerIdentity, _, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.put(digest, memoryDashboardRow{code: true, origin: origin, peer: peer, expires: expires})
	return nil
}

func (m *memoryDashboardSessions) RedeemDashboardCode(_ context.Context, code, session, origin string, now, expires time.Time) (localhostwire.LocalPeerIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[code]
	delete(m.rows, code)
	if !ok || !row.code || row.origin != origin || !now.Before(row.expires) {
		return localhostwire.LocalPeerIdentity{}, errors.New("refused")
	}
	m.put(session, memoryDashboardRow{origin: origin, peer: row.peer, expires: expires})
	return row.peer, nil
}

func (m *memoryDashboardSessions) DashboardSession(_ context.Context, digest string, now time.Time) (localhostwire.LocalPeerIdentity, string, time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[digest]
	if !ok || row.code || !now.Before(row.expires) {
		return localhostwire.LocalPeerIdentity{}, "", time.Time{}, false, nil
	}
	return row.peer, row.origin, row.expires, true, nil
}

func (m *memoryDashboardSessions) RevokeDashboardSession(_ context.Context, digest string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[digest]
	if !ok || row.code {
		return false, nil
	}
	delete(m.rows, digest)
	return now.Before(row.expires), nil
}

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
	return func(req *http.Request) { req.Header.Set("Cookie", dashboardCookie+"="+session) }
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
	login, failure := logins.mint(t.Context(), localhostwire.LocalPeerIdentity{GOOS: "linux", UID: &uid})
	if failure != nil {
		t.Fatal(failure)
	}
	response := httptest.NewRecorder()
	logins.gate(http.NotFoundHandler(), nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, login.URL, nil))
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == dashboardCookie && cookie.Value != "" {
			return cookie.Value
		}
	}
	t.Fatalf("sign-in set no session cookie: HTTP %d %s", response.Code, response.Body)
	return ""
}

func storedLogins(clock func() time.Time, origin string) *dashboardLogins {
	logins := newDashboardLogins(clock)
	logins.persist(&memoryDashboardSessions{})
	logins.serving(origin)
	return logins
}

func TestDashboardSessionForwardsUnrelatedCookies(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	request.Header.Set("Authorization", "Bearer stale")
	for _, name := range []string{"sw_session", "__Host-sw_session", dashboardCookie, "__Host-" + dashboardCookie, dashboardSignedInMarker} {
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
	logins.serveSession(httptest.NewRecorder(), request, handler, dashboardSession{digest: "d", expires: time.Now().Add(time.Hour)})
	if !forwarded {
		t.Fatal("authenticated request did not reach the handler")
	}
	if len(request.Cookies()) != 6 {
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
	signIn := func(login DashboardLogin) string {
		t.Helper()
		resp := loginRequest(t, browser, http.MethodGet, login.URL, nil)
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
			t.Fatalf("sign in: HTTP %d to %q", resp.StatusCode, resp.Header.Get("Location"))
		}
		if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("sign in headers %v", resp.Header)
		}
		cookies := (&http.Response{Header: resp.Header}).Cookies()
		if len(cookies) != 2 || cookies[1].Name != dashboardSignedInMarker || cookies[1].Value != "1" || cookies[1].HttpOnly || cookies[1].SameSite != http.SameSiteStrictMode {
			t.Fatalf("sign in set cookies %q", resp.Header.Values("Set-Cookie"))
		}
		cookie, raw := cookies[0], resp.Header.Values("Set-Cookie")[0]
		if cookie.Name != dashboardCookie || len(cookie.Value) < 43 || cookie.Path != "/" || cookie.Domain != "" || !cookie.HttpOnly || cookie.Secure ||
			cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != int(dashboardSessionLifetime/time.Second) || !strings.Contains(raw, "Expires=") {
			t.Fatalf("session cookie %q", raw)
		}
		return cookie.Value
	}

	if resp := loginRequest(t, socket, http.MethodGet, "http://sparkwing-api/api/v1/runs", nil); resp.StatusCode != http.StatusOK || api.reached() != 1 {
		t.Fatalf("api.sock without a session: HTTP %d", resp.StatusCode)
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

	session := signIn(login)
	if again := loginRequest(t, browser, http.MethodGet, login.URL, nil); again.StatusCode != http.StatusUnauthorized || len(again.Header.Values("Set-Cookie")) != 0 {
		t.Fatalf("second use: HTTP %d", again.StatusCode)
	}
	for _, edit := range []func(*http.Request){
		func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+session) },
		func(req *http.Request) { req.Header.Set("Cookie", "sw_session="+session) },
		func(req *http.Request) { req.Header.Set("Cookie", "__Host-"+dashboardCookie+"="+session) },
		func(req *http.Request) { req.Header.Set("Cookie", dashboardCookie+"="+session+"; "+dashboardCookie+"=planted") },
		withSession(session[:len(session)-1] + map[bool]string{true: "B", false: "A"}[strings.HasSuffix(session, "A")]),
	} {
		if resp := loginRequest(t, browser, http.MethodGet, runs, edit); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a presentation other than the session cookie: HTTP %d", resp.StatusCode)
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

	// safety: the session is stored, so a controller restart keeps it and only its 30-day expiry ends it.
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
	clock.advance(dashboardSessionLifetime - time.Second)
	if resp := loginRequest(t, browser, http.MethodGet, runs, withSession(session)); resp.StatusCode != http.StatusOK {
		t.Fatalf("session after a restart, inside 30 days: HTTP %d", resp.StatusCode)
	}
	clock.advance(time.Second)
	if resp := loginRequest(t, browser, http.MethodGet, runs, withSession(session)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session at 30 days: HTTP %d", resp.StatusCode)
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
	resp := loginRequest(t, browser, http.MethodPost, logout, withSessionFrom(session, base))
	cleared := resp.Header.Values("Set-Cookie")
	if resp.StatusCode != http.StatusNoContent || len(cleared) != 2 || !strings.HasPrefix(cleared[0], dashboardCookie+"=;") || !strings.HasPrefix(cleared[1], dashboardSignedInMarker+"=;") || !strings.Contains(cleared[0], "Max-Age=0") || !strings.Contains(cleared[1], "Max-Age=0") {
		t.Fatalf("logout: HTTP %d Set-Cookie %q", resp.StatusCode, cleared)
	}
	if resp := loginRequest(t, browser, http.MethodGet, runs, withSession(session)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after logout: HTTP %d", resp.StatusCode)
	}
	if resp := loginRequest(t, browser, http.MethodPost, logout, withSessionFrom(session, base)); resp.StatusCode != http.StatusUnauthorized {
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

func TestLocalDashboardLinkRedeemsOnlyAtItsOrigin(t *testing.T) {
	logins := storedLogins(nil, "http://127.0.0.1:4343")
	uid := uint32(7)
	login, failure := logins.mint(t.Context(), localhostwire.LocalPeerIdentity{GOOS: "linux", UID: &uid})
	if failure != nil {
		t.Fatal(failure)
	}
	other := strings.Replace(login.URL, "127.0.0.1:4343", "127.0.0.1:9999", 1)
	response := httptest.NewRecorder()
	logins.gate(http.NotFoundHandler(), nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, other, nil))
	if response.Code != http.StatusUnauthorized || len(response.Result().Cookies()) != 0 {
		t.Fatalf("redeemed at another port: HTTP %d", response.Code)
	}
	response = httptest.NewRecorder()
	logins.gate(http.NotFoundHandler(), nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, login.URL, nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("a link refused at another origin stayed redeemable: HTTP %d", response.Code)
	}
	session := signInThrough(t, logins)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9999/api/v1/runs", nil)
	withSession(session)(request)
	response = httptest.NewRecorder()
	logins.gate(http.NotFoundHandler(), nil).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("a session redeemed at one port authorized another: HTTP %d", response.Code)
	}
}

func TestLocalDashboardMintRefusesWithoutServingDashboard(t *testing.T) {
	logins := newDashboardLogins(nil)
	uid := uint32(7)
	if _, failure := logins.mint(t.Context(), localhostwire.LocalPeerIdentity{GOOS: "linux", UID: &uid}); failure == nil || failure.Hint == "" {
		t.Fatalf("mint without a dashboard = %v", failure)
	}
	logins.serving("http://127.0.0.1:4343")
	if _, failure := logins.mint(t.Context(), localhostwire.LocalPeerIdentity{GOOS: "linux", UID: &uid}); failure == nil {
		t.Fatal("minted without a session store")
	}
	response := httptest.NewRecorder()
	logins.socketRoutes(http.NotFoundHandler()).ServeHTTP(response, httptest.NewRequest(http.MethodPost, DashboardLoginPath, nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("mint without a verified peer: HTTP %d", response.Code)
	}
}

func FuzzLocalDashboardRequiresSessionCookie(f *testing.F) {
	server := httptest.NewServer(nil)
	f.Cleanup(server.Close)
	logins := storedLogins(nil, server.URL)
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
	f.Add("GET", "/api/v1/runs", "Bearer "+session, "", "")
	f.Add("POST", "/api/v1/runs/x/cancel", "", dashboardCookie+"="+session[:10], "")
	f.Add("GET", "/api/v1/runs", "", "sw_session="+session, "")
	f.Add("POST", "/logout", "", "", "")
	f.Add("GET", "/login", "", "", "token=")
	f.Add("GET", "/", "", dashboardCookie+"=", "")
	f.Add("DELETE", "/login/../api", "bearer x", "other=x; "+dashboardCookie+"=", "token=")
	client := noRedirects()
	var probe int
	f.Fuzz(func(t *testing.T, method, path, authorization, cookie, query string) {
		if strings.Contains(cookie, session) {
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

func signedInCookie(t *testing.T, r *localRuntime) (string, string) {
	t.Helper()
	r.mu.Lock()
	logins := r.run.logins
	r.mu.Unlock()
	session := signInThrough(t, logins)
	return dashboardCookie + "=" + session, strings.TrimPrefix(logins.servingOrigin(), "http://")
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
			logins := storedLogins(clock.Now, "http://"+server.Listener.Addr().String())
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
			logins := storedLogins(nil, origin)
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
