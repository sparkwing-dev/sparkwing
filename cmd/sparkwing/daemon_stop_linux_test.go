//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testhome"
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
	if alive, err := wingd.LocalRunnerProcessAlive(runner); err != nil || alive {
		t.Fatalf("an idle local runner outlived daemon stop: alive=%v err=%v", alive, err)
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

func TestDaemonStopRefusesASupervisedRunnerWithoutAnActiveRegistration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		home := t.TempDir()
		config, host := localServicesFixture(t, home)
		host.Stat = func(string) (os.FileInfo, error) { return os.Stat(host.Home) }
		unit := wingd.LocalRunnerServiceName(home, "starting") + ".service"
		host.Exec = func(context.Context, string, ...string) (string, error) {
			return fmt.Sprintf(`[{"unit":%q,"active":"active","sub":"running"}]`, unit), nil
		}
		services, err := composeLocalServices(t.Context(), config, host)
		if err != nil {
			t.Fatal(err)
		}
		err = services.retireRunner(t.Context())
		if err == nil || !strings.Contains(err.Error(), "shutdown is incomplete") || !strings.Contains(err.Error(), unit) {
			t.Fatalf("stop ignored a supervised runner with no active registration: %v", err)
		}
	})
}

func TestDaemonStopWaitsForSupervisedUnitCollection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		home := t.TempDir()
		config, host := localServicesFixture(t, home)
		host.Stat = func(string) (os.FileInfo, error) { return os.Stat(host.Home) }
		calls := 0
		host.Exec = func(_ context.Context, name string, args ...string) (string, error) {
			calls++
			if name != "systemctl" || args[len(args)-1] != wingd.LocalRunnerServiceName(home, "")[:len("sparkwing-runner-")+16+1]+"*.service" {
				t.Fatalf("wrong inventory: %s %v", name, args)
			}
			if calls == 1 {
				return fmt.Sprintf(`[{"unit":%q,"active":"active","sub":"running"}]`, wingd.LocalRunnerServiceName(home, "starting")+".service"), nil
			}
			return "[]", nil
		}
		services, err := composeLocalServices(t.Context(), config, host)
		if err != nil {
			t.Fatal(err)
		}
		if err := services.retireRunner(t.Context()); err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("unit collection checks = %d, want 2", calls)
		}
	})
}

func TestDaemonStopRefusesAnUnreadableSupervisedUnitInventory(t *testing.T) {
	for _, output := range []string{"null", "unreadable"} {
		t.Run(output, func(t *testing.T) {
			home := t.TempDir()
			config, host := localServicesFixture(t, home)
			host.Stat = func(string) (os.FileInfo, error) { return os.Stat(host.Home) }
			host.Exec = func(context.Context, string, ...string) (string, error) {
				if output == "unreadable" {
					return "", os.ErrPermission
				}
				return output, nil
			}
			services, err := composeLocalServices(t.Context(), config, host)
			if err != nil {
				t.Fatal(err)
			}
			if err := services.retireRunner(t.Context()); err == nil || !strings.Contains(err.Error(), "shutdown is incomplete") {
				t.Fatalf("unverified shutdown succeeded: %v", err)
			}
		})
	}
}

func TestDaemonStopChecksOnlySystemdHosts(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			home := t.TempDir()
			config, host := localServicesFixture(t, home)
			host.Stat = func(path string) (os.FileInfo, error) {
				if path != "/run/systemd/system" {
					t.Fatalf("unexpected capability check: %s", path)
				}
				if !present {
					return nil, os.ErrNotExist
				}
				return os.Stat(host.Home)
			}
			calls := 0
			host.Exec = func(context.Context, string, ...string) (string, error) {
				calls++
				return "[]", nil
			}
			services, err := composeLocalServices(t.Context(), config, host)
			if err != nil {
				t.Fatal(err)
			}
			if err := services.retireRunner(t.Context()); err != nil {
				t.Fatal(err)
			}
			if want := map[bool]int{false: 0, true: 1}[present]; calls != want {
				t.Fatalf("inventory calls=%d, want %d for systemd=%v", calls, want, present)
			}
		})
	}
}
