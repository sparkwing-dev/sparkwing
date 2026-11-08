package wingd

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func TestDashboardServeSignInStaysOnItsConfiguredOrigin(t *testing.T) {
	const host = "desk.example.ts.net"
	const origin = "https://" + host
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("dashboard: {address: '127.0.0.1:0', allowed_hosts: ["+host+", other.example.ts.net]}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
	settings, err := ResolveDashboardSettings()
	if err != nil {
		t.Fatal(err)
	}
	probe := &loginProbe{}
	f := &runtimeControllerFixture{http: func() localhostwire.LocalHTTP {
		return localhostwire.LocalHTTP{API: http.NotFoundHandler(), Dashboard: probe.handler()}
	}}
	r := newRuntimeFixture(t, f)
	r.listen, r.cfg.Dashboard = nil, settings
	lifecycle := NewLocalLifecycle(r, nil)
	status, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"})
	if err != nil {
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
	mint := func(selected string) loginReply {
		return loginRequest(t, socket, http.MethodPost, "http://sparkwing-api"+DashboardLoginPath+"?host="+url.QueryEscape(selected), nil)
	}
	var login DashboardLogin
	response := mint(host)
	if response.StatusCode != http.StatusOK || json.Unmarshal(response.Body, &login) != nil || !strings.HasPrefix(login.URL, origin+"/#code=") {
		t.Fatalf("configured Serve sign-in: HTTP %d, link origin %q", response.StatusCode, strings.Split(login.URL, "#")[0])
	}
	for _, selected := range []string{"evil.example", host + ":8443", "https://" + host, host + ".evil.example"} {
		if response := mint(selected); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("unconfigured host %q: HTTP %d", selected, response.StatusCode)
		}
	}
	browser := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	t.Cleanup(browser.CloseIdleConnections)
	request := func(method, targetHost, requestOrigin, token, body string) loginReply {
		return loginRequest(t, browser, method, status.DashboardURL+"/api/v1/runs", func(req *http.Request) {
			req.Host = targetHost
			req.Header.Set("Origin", requestOrigin)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-Forwarded-Host", host)
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("Tailscale-User-Login", "operator@example.com")
			if body != "" {
				req.URL.Path = "/login"
				req.Body = io.NopCloser(strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
			}
		})
	}
	_, code, _ := strings.Cut(login.URL, "/#code=")
	response = request(http.MethodPost, host, origin, "", `{"code":"`+code+`"}`)
	var grant dashboardSessionGrant
	if response.StatusCode != http.StatusOK || json.Unmarshal(response.Body, &grant) != nil || grant.Token == "" {
		t.Fatalf("Serve exchange: HTTP %d", response.StatusCode)
	}
	if response = request(http.MethodGet, host, "", grant.Token, ""); response.StatusCode != http.StatusOK {
		t.Fatalf("Serve authenticated read: HTTP %d", response.StatusCode)
	}
	var localLogin DashboardLogin
	response = mint("")
	if response.StatusCode != http.StatusOK || json.Unmarshal(response.Body, &localLogin) != nil {
		t.Fatal("loopback sign-in link was not preserved")
	}
	_, localCode, _ := strings.Cut(localLogin.URL, "/#code=")
	if response = request(http.MethodPost, host, origin, "", `{"code":"`+localCode+`"}`); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("loopback code exchanged on Serve: HTTP %d", response.StatusCode)
	}
	loopback, err := url.Parse(status.DashboardURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host, origin, token string
		want                int
	}{
		{host, origin, "", http.StatusUnauthorized},
		{loopback.Host, "", grant.Token, http.StatusUnauthorized},
		{"other.example.ts.net", "https://other.example.ts.net", grant.Token, http.StatusUnauthorized},
		{"evil.example", origin, grant.Token, http.StatusForbidden},
		{host, "http://" + host, grant.Token, http.StatusForbidden},
		{host, status.DashboardURL, grant.Token, http.StatusForbidden},
		{host, origin, grant.Token, http.StatusOK},
	} {
		response := request(http.MethodPost, tc.host, tc.origin, tc.token, "")
		if response.StatusCode != tc.want {
			t.Errorf("write Host=%q Origin=%q: HTTP %d, want %d", tc.host, tc.origin, response.StatusCode, tc.want)
		}
	}
}
