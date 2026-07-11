package client

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// daemonLogTailLines is how many trailing daemon-log lines the client
// folds into an error when a spawned daemon dies before serving.
const daemonLogTailLines = 8

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

	logPath, lerr := daemonLogPath(home)
	var logF *os.File
	if lerr == nil {
		logF, _ = os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}

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
