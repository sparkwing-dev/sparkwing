package client

import (
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// TestOpenDaemonLog_CreatesDirWhenMissing pins the root-cause fix: the
// daemon directory does not exist when the client opens the log at spawn,
// so openDaemonLog must create it rather than silently fail and discard
// the daemon's output.
func TestOpenDaemonLog_CreatesDirWhenMissing(t *testing.T) {
	home := t.TempDir()
	f := openDaemonLog(home)
	if f == nil {
		t.Fatal("openDaemonLog returned nil; the daemon would have no log")
	}
	if _, err := f.WriteString("hello\n"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	_ = f.Close()

	path, err := wingd.LogPath(home)
	if err != nil {
		t.Fatalf("log path: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("log not created at %s (err=%v)", path, err)
	}
}

// TestOpenDaemonLog_RotatesOncePastCap pins that a log grown past the cap
// is rotated to d.log.1 on the next spawn, keeping the file bounded.
func TestOpenDaemonLog_RotatesOncePastCap(t *testing.T) {
	home := t.TempDir()
	path, err := wingd.LogPath(home)
	if err != nil {
		t.Fatalf("log path: %v", err)
	}
	if f := openDaemonLog(home); f != nil {
		_ = f.Close()
	}
	if err := os.WriteFile(path, make([]byte, daemonLogCapBytes+1), 0o600); err != nil {
		t.Fatalf("seed oversized log: %v", err)
	}
	if f := openDaemonLog(home); f != nil {
		_ = f.Close()
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("oversized log was not rotated to %s.1: %v", path, err)
	}
}
