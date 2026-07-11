package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

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
	sock, err := wingd.SocketPath(home)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(sock), "d.log"), nil
}
