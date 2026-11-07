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
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
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
	if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(response.Header.Get("Location"), "/#s=") {
		t.Fatalf("sign-in link did not start a session: HTTP %d", response.StatusCode)
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

	registry, err := wingdclient.NewLocalRunnerClient(home, installedVersion())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := registry.Active(ctx)
	for err != nil && ctx.Err() == nil {
		time.Sleep(100 * time.Millisecond)
		runner, err = registry.Active(ctx)
	}
	if err != nil {
		t.Fatalf("the controller started no local runner: %v", err)
	}
	out.Reset()
	if err := runDaemonLifecycleCommandArgs(ctx, []string{"stop", "-o", "json"}, &out, resolveLocalServices); err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	exited, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	for {
		alive, err := wingd.LocalRunnerProcessAlive(runner)
		if err == nil && !alive {
			break
		}
		if exited.Err() != nil {
			t.Fatalf("an idle local runner outlived daemon stop: alive=%t err=%v", alive, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// safety: a runner that outlives its controller exits on failed claims instead, without recording
	// its stop or revoking its claim token.
	logs, err := filepath.Glob(filepath.Join(home, "runners", "*", "runner.log"))
	if err != nil || len(logs) == 0 {
		t.Fatalf("runner logs: %v %v", logs, err)
	}
	for _, path := range logs {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "api.sock") {
			t.Fatalf("the runner lost its controller before retiring:\n%s", body)
		}
	}
}
