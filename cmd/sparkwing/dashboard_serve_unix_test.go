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
	if err != nil || link.Path != "/" || !strings.HasPrefix(link.Fragment, "code=") {
		t.Fatalf("dashboard open printed no sign-in link: %q %v", opened.URL, err)
	}
	client := &http.Client{}
	defer client.CloseIdleConnections()
	origin := link.Scheme + "://" + link.Host
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/login", strings.NewReader(`{"code":"`+strings.TrimPrefix(link.Fragment, "code=")+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("sign-in exchange does not answer: %v", err)
	}
	var grant struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(response.Body).Decode(&grant)
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if response.StatusCode != http.StatusOK || err != nil || grant.Token == "" {
		t.Fatalf("sign-in exchange did not start a session: HTTP %d (%v)", response.StatusCode, err)
	}

	after := status()
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
