package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// hostSecretsDaemon hosts the daemon `sparkwing secrets` starts in this
// process and returns a stop that waits for it, as a restart needs.
func hostSecretsDaemon(t *testing.T, home string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	started := false
	previous := secretsDaemonOptions
	secretsDaemonOptions = func() wingdclient.Options {
		return wingdclient.Options{
			Home:    home,
			Version: "v1.0.0",
			Spawn: func(string, string) error {
				if started {
					return nil
				}
				started = true
				go func() {
					done <- orchestrator.RunWingdDaemon(ctx, orchestrator.WingdOptions{Home: home, Version: "v1.0.0"})
				}()
				return nil
			},
		}
	}
	stop := func() {
		secretsDaemonOptions = previous
		cancel()
		if started {
			started = false
			if err := <-done; err != nil {
				t.Errorf("daemon: %v", err)
			}
		}
	}
	t.Cleanup(stop)
	return stop
}

// secretsHome points this test at a fresh sparkwing home and config
// directory, and returns both.
func secretsHome(t *testing.T) (home, configDir string) {
	t.Helper()
	if testing.Short() {
		t.Skip("slow: hosts the admission daemon")
	}
	home = queueHome(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv(localsecrets.KeyFileEnv, filepath.Join(home, "secrets.key"))
	t.Setenv(localsecrets.KeyEnv, "")
	t.Setenv("SPARKWING_SECRETS", "")
	t.Setenv("SPARKWING_CONFIG_ENV", "")
	if err := os.MkdirAll(paths.TestSandbox(), 0o700); err != nil {
		t.Fatal(err)
	}
	xdg, err := os.MkdirTemp(paths.TestSandbox(), "xdg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(xdg) })
	t.Setenv("XDG_CONFIG_HOME", xdg)
	configDir = filepath.Join(xdg, "sparkwing")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return home, configDir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSecretsWithoutAProfileUseTheDaemonsSealedStore(t *testing.T) {
	home, _ := secretsHome(t)
	hostSecretsDaemon(t, home)

	if out := captureStdout(t, func() {
		if err := runSecretList(nil); err != nil {
			t.Fatalf("list before any secret: %v", err)
		}
	}); !strings.Contains(out, "no secrets") {
		t.Fatalf("list on a fresh home = %q, want none", out)
	}
	captureStdout(t, func() {
		if err := runSecretSet([]string{"--name", "TOKEN", "--value", "abc123"}); err != nil {
			t.Fatalf("set: %v", err)
		}
		if err := runSecretSet([]string{"--name", "REGION", "--value", "us-east-1", "--plain"}); err != nil {
			t.Fatalf("set --plain: %v", err)
		}
	})
	if got := captureStdout(t, func() {
		if err := runSecretGet([]string{"--name", "TOKEN"}); err != nil {
			t.Fatalf("get: %v", err)
		}
	}); got != "abc123" {
		t.Fatalf("get TOKEN = %q, want abc123", got)
	}
	listed := captureStdout(t, func() {
		if err := runSecretList(nil); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	if !strings.Contains(listed, "TOKEN") || !strings.Contains(listed, "REGION") || strings.Contains(listed, "abc123") {
		t.Fatalf("list = %q, want both names and no masked value", listed)
	}

	st, err := store.OpenReadOnly(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for name, masked := range map[string]bool{"TOKEN": true, "REGION": false} {
		sec, err := st.GetSecretRow(name, "")
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !secrets.IsBound(sec.Value) || sec.Masked != masked || !sec.Shared {
			t.Errorf("%s stored as %q masked %v shared %v; want sealed, masked %v, shared", name, sec.Value, sec.Masked, sec.Shared, masked)
		}
	}
	_ = st.Close()

	captureStdout(t, func() {
		if err := runSecretDelete([]string{"--name", "TOKEN"}); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})
	if err := runSecretGet([]string{"--name", "TOKEN"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("get after delete = %v, want not found", err)
	}
}

// An upgrade finds the per-file settings, the dotenv secret files and a runs
// store holding history, and imports the dotenv files once.
func TestUpgradeImportsLegacySettingsAndDotenvSecretsOnce(t *testing.T) {
	home, dir := secretsHome(t)
	writeFiles(t, dir, map[string]string{
		"profiles.yaml": "profiles:\n  prod:\n    controller: {url: https://api.example}\n",
		"secrets.env":   "TOKEN=from-dotenv\n",
		"config.env":    "REGION=us-east-1\n",
	})
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	configPath, err := userconfig.Path()
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := profile.Load(configPath); err != nil || cfg.Profiles["prod"] == nil {
		t.Fatalf("profiles after the upgrade = %+v, %v; want prod copied from profiles.yaml", cfg, err)
	}
	stop := hostSecretsDaemon(t, home)
	listed := captureStdout(t, func() {
		if err := runSecretList(nil); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	if !strings.Contains(listed, "TOKEN") || !strings.Contains(listed, "REGION") {
		t.Fatalf("list after the upgrade = %q, want both imported names", listed)
	}

	captureStdout(t, func() {
		if err := runSecretDelete([]string{"--name", "TOKEN"}); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})
	writeFiles(t, dir, map[string]string{"secrets.env": "TOKEN=from-dotenv\nLATER=added\n"})
	stop()
	hostSecretsDaemon(t, home)
	listed = captureStdout(t, func() {
		if err := runSecretList(nil); err != nil {
			t.Fatalf("list after a restart: %v", err)
		}
	})
	if strings.Contains(listed, "TOKEN") || strings.Contains(listed, "LATER") || !strings.Contains(listed, "REGION") {
		t.Fatalf("list after a restart = %q, want REGION alone: the import runs once", listed)
	}
}

func TestUpgradeWithAMalformedDotenvFileFailsSecretsUntilFixed(t *testing.T) {
	home, dir := secretsHome(t)
	writeFiles(t, dir, map[string]string{"secrets.env": "TOKEN=fine\n", "config.env": "not a dotenv line\n"})
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	stop := hostSecretsDaemon(t, home)
	if err := runSecretList(nil); err == nil || !strings.Contains(err.Error(), "config.env:1") {
		t.Fatalf("list with a malformed config.env = %v, want the import error naming config.env:1", err)
	}
	if err := runSecretSet([]string{"--name", "OTHER", "--value", "x"}); err == nil {
		t.Fatal("set succeeded while the import had failed")
	}

	writeFiles(t, dir, map[string]string{"config.env": "REGION=us-east-1\n"})
	stop()
	hostSecretsDaemon(t, home)
	listed := captureStdout(t, func() {
		if err := runSecretList(nil); err != nil {
			t.Fatalf("list after the fix and a restart: %v", err)
		}
	})
	if !strings.Contains(listed, "TOKEN") || !strings.Contains(listed, "REGION") {
		t.Fatalf("list after the fix = %q, want both names", listed)
	}
}

// Dotenv secrets on a machine that never ran a pipeline: no state.db yet.
func TestSecretsListImportsIntoAFreshHome(t *testing.T) {
	home, dir := secretsHome(t)
	writeFiles(t, dir, map[string]string{"secrets.env": "TOKEN=from-dotenv\n"})
	hostSecretsDaemon(t, home)
	listed := captureStdout(t, func() {
		if err := runSecretList(nil); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	if !strings.Contains(listed, "TOKEN") {
		t.Fatalf("list on a fresh home with secrets.env = %q, want TOKEN imported", listed)
	}
}
