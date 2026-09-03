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
