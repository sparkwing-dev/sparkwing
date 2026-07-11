package wingd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func deepHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), strings.Repeat("nested-segment/", 12))
	return home
}

// TestSocketPath_StaysShortForDeepHome pins BW-649: a home deep enough
// that the old under-home socket would exceed sun_path still resolves to a
// short socket path, while the lock and state stay under the home.
func TestSocketPath_StaysShortForDeepHome(t *testing.T) {
	home := deepHome(t)

	underHome := filepath.Join(home, "wingd", "d.sock")
	if len(underHome) < maxSunPath() {
		t.Fatalf("test home not deep enough: under-home socket is %d bytes, need >= %d", len(underHome), maxSunPath())
	}

	sock, err := SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(sock) >= maxSunPath() {
		t.Fatalf("resolved socket %q is %d bytes, over the %d-byte limit", sock, len(sock), maxSunPath())
	}
	if !strings.HasPrefix(sock, socketBaseDir()) {
		t.Errorf("socket %q not under socket base %q", sock, socketBaseDir())
	}

	lock, _ := LockPath(home)
	if !strings.HasPrefix(lock, home) {
		t.Errorf("lock %q left the home %q", lock, home)
	}
}

// TestSocketPath_DistinctPerHome ensures two homes get distinct sockets so
// each keeps its own daemon.
func TestSocketPath_DistinctPerHome(t *testing.T) {
	a, _ := SocketPath(t.TempDir())
	b, _ := SocketPath(t.TempDir())
	if a == b {
		t.Fatalf("distinct homes shared socket %q", a)
	}
}

// TestValidateSocketPath_RejectsOverLength asserts the length guard names
// the limit and the path rather than letting bind fail with a bare EINVAL.
func TestValidateSocketPath_RejectsOverLength(t *testing.T) {
	long := "/tmp/" + strings.Repeat("x", maxSunPath())
	err := ValidateSocketPath(long)
	if err == nil {
		t.Fatal("expected an over-length socket path to be rejected")
	}
	if !strings.Contains(err.Error(), long) {
		t.Errorf("error should name the path, got: %v", err)
	}
	if ValidateSocketPath("/tmp/sparkwing-0-abc/d.sock") != nil {
		t.Error("a short socket path should validate")
	}
}

// TestDaemon_BindsUnderDeepHome drives a real daemon whose home is deep
// enough to have broken the old under-home socket, and asserts it reaches
// the serving state.
func TestDaemon_BindsUnderDeepHome(t *testing.T) {
	home := deepHome(t)
	d, err := New(Config{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- d.Run(ctx) }()

	select {
	case <-d.Ready():
	case err := <-errc:
		cancel()
		t.Fatalf("daemon exited before serving under deep home: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("daemon never became ready under deep home")
	}
	cancel()
	select {
	case <-errc:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop after cancel")
	}
}
