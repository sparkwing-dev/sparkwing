package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// daemonLogTailLines is how many trailing daemon-log lines the client
// folds into an error when a spawned daemon dies before serving.
const daemonLogTailLines = 8

// daemonLogCapBytes is the size past which the daemon log is rotated once
// (to d.log.1) at spawn, so a long-lived home cannot grow it without
// bound. One rotation keeps the previous run's tail for a post-mortem.
const daemonLogCapBytes = 1 << 20

// defaultSpawn re-execs this binary as a detached `sparkwing wingd run`
// for home. The daemon's stdout and stderr go to a log file beside its
// socket. Racing spawns are safe: the daemon's flock election lets only
// one win, and the losers exit cleanly.
func defaultSpawn(home, version string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	args := []string{"wingd", "run"}
	if home != "" {
		args = append(args, "--home", home)
	}
	if version != "" {
		args = append(args, "--version", version)
	}

	logF := openDaemonLog(home)

	cmd := exec.Command(self, args...)
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Env = os.Environ()
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		if logF != nil {
			_ = logF.Close()
		}
		return fmt.Errorf("start daemon: %w", err)
	}
	_ = cmd.Process.Release()
	if logF != nil {
		_ = logF.Close()
	}
	return nil
}

func daemonLogPath(home string) (string, error) {
	return wingd.LogPath(home)
}

// openDaemonLog prepares the daemon's log file for a detached spawn: it
// creates the daemon directory (which the spawned daemon has not yet made
// when the client opens the file), rotates the log once if it has grown
// past the cap, and opens it append-only. The spawned daemon's stdout and
// stderr are pointed at the returned file, so its operational log and any
// early crash both land at the documented path. Nil on failure leaves the
// daemon's output discarded rather than blocking the spawn.
func openDaemonLog(home string) *os.File {
	path, err := wingd.LogPath(home)
	if err != nil {
		return nil
	}
	if dir, derr := wingd.StateDir(home); derr == nil {
		_ = os.MkdirAll(dir, 0o700)
	} else {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if fi, serr := os.Stat(path); serr == nil && fi.Size() > daemonLogCapBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// daemonLogTail returns the last few non-empty lines of home's daemon log,
// or "" when it is absent or empty. It lets the client attach a
// startup-death cause the daemon wrote to its own log.
func daemonLogTail(home string) string {
	path, err := wingd.LogPath(home)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > daemonLogTailLines {
		lines = lines[len(lines)-daemonLogTailLines:]
	}
	return strings.Join(lines, "\n")
}
