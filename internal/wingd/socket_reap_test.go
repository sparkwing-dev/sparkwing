//go:build !windows

package wingd

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A takeover binds a fresh socket at the path a predecessor left dead, which
// is the window a sweep that classified the path dead by dialing it can
// unlink out from under the successor.
func TestReapSocketDir_KeepsASocketReboundAfterTheDeadDial(t *testing.T) {
	home := t.TempDir()
	sock := bindPlaceholderSocket(t, home)

	if _, dead := socketStatus(sock); !dead {
		t.Fatal("placeholder socket was not classified dead")
	}

	if err := os.Remove(sock); err != nil {
		t.Fatalf("successor could not clear the stale socket: %v", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("successor could not bind: %v", err)
	}
	defer func() { _ = ln.Close() }()

	reapSocketDir(sock)

	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("sweep unlinked a successor daemon's socket: %v", err)
	}
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Fatalf("successor is no longer reachable after the sweep: %v", err)
	}
	_ = c.Close()
}

func TestReapSocketDir_StillClearsASocketNothingServes(t *testing.T) {
	home := t.TempDir()
	sock := bindPlaceholderSocket(t, home)

	reapSocketDir(sock)

	if _, err := os.Stat(filepath.Dir(sock)); !os.IsNotExist(err) {
		t.Fatalf("dead socket directory survived the sweep: %v", err)
	}
}

// The API socket sits beside the admission socket and was unlinked with no
// liveness check of its own, so a daemon serving it lost it to any sweep that
// found the admission socket dead.
func TestReapSocketDir_KeepsALiveAPISocket(t *testing.T) {
	home := t.TempDir()
	sock := bindPlaceholderSocket(t, home)
	api := APISocketBeside(sock)

	ln, err := net.Listen("unix", api)
	if err != nil {
		t.Fatalf("bind api socket: %v", err)
	}
	defer func() { _ = ln.Close() }()

	reapSocketDir(sock)

	if _, err := os.Stat(api); err != nil {
		t.Fatalf("sweep unlinked a live api socket: %v", err)
	}
	c, err := net.DialTimeout("unix", api, time.Second)
	if err != nil {
		t.Fatalf("api socket is no longer reachable after the sweep: %v", err)
	}
	_ = c.Close()
	if _, err := os.Stat(filepath.Dir(sock)); err != nil {
		t.Fatalf("sweep removed the directory a live api socket sits in: %v", err)
	}
}

func TestSocketStillDead_TreatsAVanishedSocketAsAlive(t *testing.T) {
	sock := bindPlaceholderSocket(t, t.TempDir())
	before, err := os.Lstat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}

	if socketStillDead(sock, before) {
		t.Fatal("a socket that vanished between the dial and the unlink was called dead; that is the successor's remove-then-listen window")
	}
}
