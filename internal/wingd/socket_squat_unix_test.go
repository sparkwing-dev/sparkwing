//go:build !windows

package wingd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDaemonMovesOffASquattedSocketDirectory(t *testing.T) {
	home := t.TempDir()
	squatted, err := EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(squatted)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o700)
		_ = os.RemoveAll(dir)
	})
	// safety: a directory with the wrong mode stands in for one another account
	// planted, which only root can create in a test.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	d, err := New(Config{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := d.bindListener()
	if err != nil {
		t.Fatalf("a squatted socket directory blocked the daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.RemoveAll(filepath.Dir(d.layout.sock))
	})
	moved := d.layout.sock
	if moved == squatted || ln.Addr().String() != moved {
		t.Fatalf("daemon serves %s, socket path %s, squatted %s", ln.Addr(), moved, squatted)
	}
	if again, err := SocketPath(home); err != nil || again != moved {
		t.Fatalf("clients resolve %q (%v), want the daemon's new socket %q", again, err, moved)
	}
	if err := ValidateSocketDir(moved); err != nil {
		t.Fatal(err)
	}
}
