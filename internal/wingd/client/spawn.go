package client

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const daemonLogTailLines = 8

const DaemonSpawnVerb = "supervise"

func defaultSpawn(home, version string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	return spawnDetached(self, home, version)
}

const hostStartGrace = 2 * time.Second

const hostStartPoll = 20 * time.Millisecond

var ErrDaemonHostFailed = errors.New("wingd/client: the daemon host binary exited without serving")

func spawnDetached(bin, home, version string) error {
	args := []string{"wingd", DaemonSpawnVerb}
	if home != "" {
		args = append(args, "--home", home)
	}
	if version != "" {
		args = append(args, "--version", version)
	}

	logF, logExisted := openDaemonLog(home)

	cmd := exec.Command(bin, args...)
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Env = os.Environ()
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		// safety: a log this spawn created and never wrote to would imply a
		// daemon ran here; leave the directory as it was found instead.
		discardUnusedDaemonLog(logF, home, logExisted)
		return fmt.Errorf("start daemon: %w", err)
	}
	if logF != nil {
		_ = logF.Close()
	}
	if err := watchHostStart(cmd, bin, home); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

func watchHostStart(cmd *exec.Cmd, bin, home string) error {
	sock, serr := wingd.SocketPath(home)
	if serr != nil {
		return nil
	}
	before, _ := os.Stat(sock)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(hostStartGrace)
	for {
		select {
		case werr := <-exited:
			var exit *exec.ExitError
			if werr == nil || !errors.As(werr, &exit) {

				return nil
			}
			return hostExitedEarly(bin, home, exit)
		default:
		}
		if now, err := os.Stat(sock); err == nil && (before == nil || !os.SameFile(before, now)) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		time.Sleep(hostStartPoll)
	}
}

func hostExitedEarly(bin, home string, exit *exec.ExitError) error {
	if tail := daemonLogTail(home); tail != "" {
		path, _ := wingd.LogPath(home)
		return fmt.Errorf("%w: %s %s (exit %d); %s:\n%s",
			ErrDaemonHostFailed, bin, exit.String(), exit.ExitCode(), path, tail)
	}
	return fmt.Errorf("%w: %s %s (exit %d)", ErrDaemonHostFailed, bin, exit.String(), exit.ExitCode())
}

func discardUnusedDaemonLog(logF *os.File, home string, existed bool) {
	if logF == nil {
		return
	}
	_ = logF.Close()
	if existed {
		return
	}
	if path, err := wingd.LogPath(home); err == nil {
		if fi, serr := os.Stat(path); serr == nil && fi.Size() == 0 {
			_ = os.Remove(path)
		}
	}
}

func openDaemonLog(home string) (f *os.File, existed bool) {
	path, err := wingd.LogPath(home)
	if err != nil {
		return nil, false
	}
	if dir, derr := wingd.StateDir(home); derr == nil {
		_ = os.MkdirAll(dir, 0o700)
	} else {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	_, _ = wingd.RotateLogOverCap(home)
	_, serr := os.Stat(path)
	existed = serr == nil
	f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, existed
	}
	return f, existed
}

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
