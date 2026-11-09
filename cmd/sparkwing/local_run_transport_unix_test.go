//go:build unix

package main

import (
	"context"
	"errors"
	"net/http/httptrace"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testhome"
)

func TestStandaloneRunClosesUnusedClientDialBeforeControllerDrain(t *testing.T) {
	user := testhome.TempDir(t)
	testhome.Set(t, user)
	home := filepath.Join(user, ".sparkwing")
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(home, "secrets.key"))
	t.Setenv("SPARKWING_CONFIG", filepath.Join(user, "missing.yaml"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, ".config"))
	repo := t.TempDir()
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines: []\n", 0o600)
	writeSnapshotFile(t, repo, ".sparkwing/main.go", "package main\nfunc main() {}\n", 0o600)
	t.Chdir(repo)
	runtime, err := resolveLocalRunRuntime(t.Context(), localRunOptions{standalone: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.close(); err != nil {
			t.Error(err)
		}
	})
	if err := runtime.ensureController(t.Context()); err != nil {
		t.Fatal(err)
	}

	connected, release := make(chan error, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		// bug: a cancelled request can leave its detached dial unused in the pool.
		ConnectDone: func(_, _ string, err error) { connected <- err; <-release },
	})
	requested := make(chan error, 1)
	go func() { _, err := runtime.controller.ReadRun(ctx, "missing"); requested <- err }()
	if err := <-connected; err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-requested; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
	unblock()
	if err := runtime.close(); err != nil {
		t.Fatalf("standalone shutdown with an unused client dial: %v", err)
	}
}
