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
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func hostSecretsDaemon(t *testing.T, home string) {
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
	t.Cleanup(func() {
		secretsDaemonOptions = previous
		cancel()
		if started {
			if err := <-done; err != nil {
				t.Errorf("daemon: %v", err)
			}
		}
	})
}

func TestSecretsWithoutAProfileUseTheDaemonsSealedStore(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: hosts the admission daemon")
	}
	home := queueHome(t)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv(localsecrets.KeyFileEnv, filepath.Join(home, "secrets.key"))
	t.Setenv(localsecrets.KeyEnv, "")
	if err := os.MkdirAll(paths.TestSandbox(), 0o700); err != nil {
		t.Fatal(err)
	}
	xdg, err := os.MkdirTemp(paths.TestSandbox(), "xdg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(xdg) })
	t.Setenv("XDG_CONFIG_HOME", xdg)
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
