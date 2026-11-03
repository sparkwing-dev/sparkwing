//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
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
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
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
	if before := status(); before.State != "running" || before.Dashboard != "not_serving" || before.URL != "" {
		t.Fatalf("an on-demand controller should run without a dashboard: %+v", before)
	}

	var out bytes.Buffer
	err = runDashboardCommandArgs(ctx, []string{"open", "-o", "json"}, &out, runtime.GOOS, func(ctx context.Context) (dashboardServices, error) {
		services, err := resolveLocalServices(ctx)
		services.options.DashboardAddress = "127.0.0.1:0"
		return services, err
	})
	if err != nil {
		t.Fatalf("dashboard open against a controller that is not serving: %v", err)
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
	if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(response.Header.Get("Location"), "/#s=") {
		t.Fatalf("sign-in link did not start a session: HTTP %d", response.StatusCode)
	}

	after := status()
	origin := link.Scheme + "://" + link.Host
	if after.State != "running" || after.PID != started.PID || after.Dashboard != "serving" || after.URL != origin {
		t.Fatalf("status should report the same controller running and the dashboard serving at %s: %+v", origin, after)
	}

	// safety: a drain outside the CLI's stop command exits the way an idle controller does.
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

	out.Reset()
	if err := runDashboardCommandArgs(ctx, []string{"stop", "-o", "json"}, &out, runtime.GOOS, resolveLocalServices); err != nil {
		t.Fatalf("dashboard stop: %v", err)
	}
	if _, err := ensureLocalController(ctx, home, func(ctx context.Context) (dashboardServices, error) {
		return resolveLocalServicesAt(ctx, home)
	}); err != nil {
		t.Fatalf("on-demand controller start after stop: %v", err)
	}
	if stopped := status(); stopped.State != "running" || stopped.Dashboard != "not_serving" {
		t.Fatalf("an explicitly stopped dashboard came back: %+v", stopped)
	}
}
