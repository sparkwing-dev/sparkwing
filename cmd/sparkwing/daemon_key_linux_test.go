//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestManagedControllerPreservesKeyAcrossRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real controller")
	}
	cli := buildSubmitCLI(t)
	for _, mode := range []string{"file", "default"} {
		t.Run(mode, func(t *testing.T) { testManagedControllerKeyRestart(t, cli, mode) })
	}
}

func testManagedControllerKeyRestart(t *testing.T, cli, mode string) {
	user := t.TempDir()
	home := filepath.Join(user, "state")
	t.Setenv("HOME", user)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_SECRETS_KEY", "")
	t.Setenv("SPARKWING_SECRETS_PREVIOUS_KEY", "")
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(user, "selected.key"))
	config := filepath.Join(user, "selected.yaml")
	if err := os.WriteFile(config, []byte("dashboard: {address: '127.0.0.1:0'}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", config)

	if mode == "default" {
		t.Setenv("SPARKWING_CONFIG", "")
		if err := os.Unsetenv("SPARKWING_CONFIG"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SPARKWING_SECRETS_KEY_FILE", "")
		config = filepath.Join(user, "config", "sparkwing", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config, []byte("dashboard: {address: '127.0.0.1:0'}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	host, err := localServiceHost(home)
	if err != nil {
		t.Fatal(err)
	}
	host.Exec = func(context.Context, string, ...string) (string, error) { return "", nil }
	manager, err := wingd.NewManagedUnitManager(host)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.InstallController(t.Context(), wingd.LocalStartOptions{Home: home, Executable: cli, Version: installedVersion()}); err != nil {
		t.Fatal(err)
	}
	units, err := manager.Inspect(t.Context())
	if err != nil || len(units) != 1 {
		t.Fatalf("units: %v, %v", units, err)
	}
	body, err := os.ReadFile(units[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	// safety: even a broken service definition must never select the operator's home.
	environment := []string{"HOME=" + user, "XDG_CONFIG_HOME=" + host.ConfigHome}
	for line := range strings.SplitSeq(string(body), "\n") {
		if assignment, ok := strings.CutPrefix(line, "Environment="); ok {
			if strings.HasPrefix(assignment, "\"") {
				assignment, err = strconv.Unquote(assignment)
				if err != nil {
					t.Fatal(err)
				}
			}
			environment = append(environment, strings.ReplaceAll(assignment, "%%", "%"))
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := procenv.ControlCommandContext(ctx, environment, cli, "wingd", "run", "--home", home)
	var logs bytes.Buffer
	command.Stdout, command.Stderr = &logs, &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	offlineStopDaemon(t, home)
	if err := pollLocalRunner(ctx, func() bool {
		status, err := wingdclient.QueryLocalController(ctx, home)
		return err == nil && status.Running
	}); err != nil {
		t.Fatal(err)
	}
	restart := procenv.ControlCommandContext(ctx, os.Environ(), cli, "daemon", "restart", "--force", "-o", "json")
	output, err := restart.CombinedOutput()
	if err != nil {
		t.Fatalf("restart cannot reopen service signing key: %v\n%s", err, output)
	}
	if mode == "file" {
		if _, err := os.Stat(os.Getenv("SPARKWING_SECRETS_KEY_FILE")); err != nil {
			t.Fatalf("service did not use selected key: %v", err)
		}
	}
	status, err := wingdclient.QueryLocalController(ctx, home)
	if err != nil || status.DashboardURL == "" || status.DashboardError != "" {
		t.Fatalf("service lost dashboard configuration: %+v, %v", status, err)
	}
}
