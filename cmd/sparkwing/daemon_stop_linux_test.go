//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/testhome"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestDaemonStopRetiresTheIdleLocalRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a detached local controller and runner; run without -short")
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
	if _, err := ensureLocalController(ctx, home, func(ctx context.Context) (dashboardServices, error) {
		return resolveLocalServicesAt(ctx, home)
	}); err != nil {
		t.Fatalf("on-demand controller start: %v", err)
	}
	registry, err := wingdclient.NewLocalRunnerClient(home, installedVersion())
	if err != nil {
		t.Fatal(err)
	}
	var runner localhostwire.LocalRunnerInstance
	if err := pollLocalRunner(ctx, func() bool {
		runner, err = registry.Active(ctx)
		return err == nil
	}); err != nil {
		t.Fatalf("the controller started no local runner: %v", err)
	}
	var out bytes.Buffer
	if err := runDaemonLifecycleCommandArgs(ctx, []string{"stop", "-o", "json"}, &out, resolveLocalServices); err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	exited, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	if err := pollLocalRunner(exited, func() bool {
		alive, err := wingd.LocalRunnerProcessAlive(runner)
		return err == nil && !alive
	}); err != nil {
		t.Fatalf("an idle local runner outlived daemon stop: %v", err)
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
