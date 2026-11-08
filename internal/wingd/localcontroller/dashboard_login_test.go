package localcontroller

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestDashboardLoginAuthorizesControllerAPIAsMintingPeer(t *testing.T) {
	home := t.TempDir()
	factory := Config{Bind: func(_ context.Context, b Binding) (Services, error) {
		services := serviceFixture(b)
		services.HTTP = controller.New(b.Store, nil).WithEngineController(b.Controller).HTTP()
		return services, nil
	}}
	// safety: a fixed port lets the restart below serve the same origin the session was redeemed at.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := wingd.NewLocalRuntime(wingd.LocalRuntimeConfig{Admission: wingd.Config{Home: home}, Dashboard: wingd.DashboardSettings{Address: address, AllowedHosts: []string{"desk.example.ts.net"}}, Open: factory.Open})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := wingd.NewLocalLifecycle(runtime, nil)
	start := wingd.LocalStartOptions{Home: home, Version: "v0.71.0"}
	if _, err := lifecycle.EnsureStarted(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	login, err := wingclient.MintDashboardLogin(t.Context(), home, "")
	if err != nil {
		t.Fatal(err)
	}
	serveLogin, err := wingclient.MintDashboardLogin(t.Context(), home, "desk.example.ts.net")
	if err != nil || !strings.HasPrefix(serveLogin.URL, "https://desk.example.ts.net/#code=") {
		t.Fatalf("client selected Serve origin: %v", err)
	}
	if _, err := wingclient.MintDashboardLogin(t.Context(), home, "unknown.example.ts.net"); err == nil {
		t.Fatal("client minted an unconfigured origin")
	}
	origin, code, _ := strings.Cut(login.URL, "/#code=")
	local, err := lifecycle.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := wingclient.QueryLocalController(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []wingd.LocalStatus{local, remote} {
		if status.DashboardURL == "" || status.DashboardURL != origin || strings.Contains(status.DashboardURL, "token") {
			t.Fatalf("status dashboard URL %q, want the sign-in origin %q", status.DashboardURL, origin)
		}
	}
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(browser.CloseIdleConnections)
	request := func(method, target, session string, status int, cookies ...*http.Cookie) []byte {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if session != "" {
			r.Header.Set("Authorization", "Bearer "+session)
		}
		if method != http.MethodGet {
			r.Header.Set("Origin", origin)
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		resp, err := browser.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != status {
			t.Fatalf("%s %s: HTTP %d, want %d: %s (%v)", method, target, resp.StatusCode, status, body, err)
		}
		return body
	}
	link, err := url.Parse(login.URL)
	if err != nil {
		t.Fatal(err)
	}
	signIn := func(code string) string {
		t.Helper()
		exchange, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin+"/login", strings.NewReader(`{"code":"`+code+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		exchange.Header.Set("Origin", origin)
		exchange.Header.Set("Content-Type", "application/json")
		resp, err := browser.Do(exchange)
		if err != nil {
			t.Fatal(err)
		}
		var grant struct {
			Token string `json:"token"`
		}
		err = json.NewDecoder(resp.Body).Decode(&grant)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil || grant.Token == "" || len(resp.Header.Values("Set-Cookie")) != 0 {
			t.Fatalf("dashboard sign-in exchange: HTTP %d, Set-Cookie %q (%v)", resp.StatusCode, resp.Header.Values("Set-Cookie"), err)
		}
		return grant.Token
	}
	session := signIn(code)
	base := link.Scheme + "://" + link.Host
	request(http.MethodGet, base+"/api/v1/runs", "", http.StatusUnauthorized, &http.Cookie{Name: "sw_dashboard", Value: session})
	request(http.MethodGet, base+"/api/v1/runs", "", http.StatusUnauthorized)
	request(http.MethodGet, base+"/api/v1/runs", session, http.StatusOK)
	var identity struct {
		Principal string `json:"principal"`
	}
	if err := json.Unmarshal(request(http.MethodGet, base+"/api/v1/auth/whoami", session, http.StatusOK), &identity); err != nil {
		t.Fatal(err)
	}
	subject, err := wingd.CurrentLocalSubject()
	if err != nil || identity.Principal != subject {
		t.Fatalf("dashboard principal %q, want minting peer %q (%v)", identity.Principal, subject, err)
	}
	for _, name := range []string{"sw_session", "__Host-sw_session"} {
		request(http.MethodGet, base+"/api/v1/runs", session, http.StatusOK, &http.Cookie{Name: name, Value: "swses_stale_other_port"})
	}
	request(http.MethodPost, base+"/logout", session, http.StatusNoContent)
	request(http.MethodGet, base+"/api/v1/runs", session, http.StatusUnauthorized)

	again, err := wingclient.MintDashboardLogin(t.Context(), home, "")
	if err != nil {
		t.Fatal(err)
	}
	_, code, _ = strings.Cut(again.URL, "/#code=")
	session = signIn(code)
	request(http.MethodGet, base+"/api/v1/runs", session, http.StatusOK)
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.EnsureStarted(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, base+"/api/v1/runs", session, http.StatusUnauthorized)
}
