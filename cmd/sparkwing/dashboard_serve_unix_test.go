//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/testhome"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestDashboardServesAControllerARunStarted(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a detached local controller; run without -short")
	}
	testhome.Set(t, testhome.TempDir(t))
	t.Setenv("XDG_CONFIG_HOME", testhome.TempDir(t))
	home := testhome.TempDir(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(home, "secrets.key"))
	config := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(config, []byte("dashboard: {address: '127.0.0.1:0'}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", config)
	offlineStopDaemon(t, home)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	started, err := ensureLocalController(ctx, home, func(ctx context.Context) (dashboardServices, error) {
		return resolveLocalServicesAt(ctx, home)
	})
	if err != nil {
		t.Fatalf("on-demand controller start: %v", err)
	}
	status := func() dashboardResult {
		t.Helper()
		var out bytes.Buffer
		if err := runDashboardCommandArgs(ctx, []string{"status", "-o", "json"}, &out, runtime.GOOS, resolveLocalServices); err != nil {
			t.Fatalf("dashboard status: %v", err)
		}
		var result dashboardResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("dashboard status %q: %v", &out, err)
		}
		return result
	}
	if before := status(); before.State != "running" || before.Dashboard != "serving" || before.URL == "" {
		t.Fatalf("an on-demand controller should serve its dashboard: %+v", before)
	}

	var out bytes.Buffer
	if err := runDashboardCommandArgs(ctx, []string{"open", "-o", "json"}, &out, runtime.GOOS, resolveLocalServices); err != nil {
		t.Fatalf("dashboard open: %v", err)
	}
	var opened dashboardLoginResult
	if err := json.Unmarshal(out.Bytes(), &opened); err != nil {
		t.Fatalf("dashboard open receipt: %v", err)
	}
	link, err := url.Parse(opened.URL)
	if err != nil || link.Path != "/login" || link.Query().Get("token") == "" {
		t.Fatalf("dashboard open printed no sign-in link: %v", err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, opened.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("sign-in link does not answer: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	signedIn := slices.ContainsFunc(response.Cookies(), func(c *http.Cookie) bool { return c.Name == "sw_dashboard" && c.Value != "" && c.HttpOnly })
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/" || !signedIn {
		t.Fatalf("sign-in link did not start a session: HTTP %d, Set-Cookie %q", response.StatusCode, response.Header.Values("Set-Cookie"))
	}

	after := status()
	origin := link.Scheme + "://" + link.Host
	if after.State != "running" || after.PID != started.PID || after.Dashboard != "serving" || after.URL != origin {
		t.Fatalf("status should report the same controller running and the dashboard serving at %s: %+v", origin, after)
	}

	if err := wingdclient.Stop(ctx, wingdclient.Options{Home: home}); err != nil {
		t.Fatalf("drain the controller: %v", err)
	}
	if _, err := ensureLocalController(ctx, home, func(ctx context.Context) (dashboardServices, error) {
		return resolveLocalServicesAt(ctx, home)
	}); err != nil {
		t.Fatalf("on-demand controller restart: %v", err)
	}
	if restarted := status(); restarted.State != "running" || restarted.PID == started.PID || restarted.Dashboard != "serving" {
		t.Fatalf("a controller restarted on demand should serve its dashboard again: %+v", restarted)
	}
}
