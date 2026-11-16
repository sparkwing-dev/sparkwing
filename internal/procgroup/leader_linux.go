//go:build linux

package procgroup

import (
	"errors"

	"golang.org/x/sys/unix"
)

func waitLeaderExit(pid int) error {
	_, err := leaderExit(pid, unix.WEXITED|unix.WNOWAIT)
	return err
}

func leaderExitedNow(pid int) (bool, error) {
	return leaderExit(pid, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT)
}

func leaderExit(pid, options int) (bool, error) {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, options, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return info.Signo != 0, err
	}
}
