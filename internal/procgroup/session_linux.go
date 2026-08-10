//go:build linux

package procgroup

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func guardedSessionSupport() error { return nil }

func sessionIdentity(pid int) (int, string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", err
	}
	line := string(data)
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+2 >= len(line) {
		return 0, "", fmt.Errorf("malformed process stat for %d", pid)
	}
	fields := strings.Fields(line[rparen+2:])
	if len(fields) < 20 {
		return 0, "", fmt.Errorf("short process stat for %d", pid)
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("process %d start time: %w", pid, err)
	}
	sid, err := unix.Getsid(pid)
	if err != nil {
		return 0, "", err
	}
	return sid, strconv.FormatUint(start, 10), nil
}

func signalGuardSession(sessionID int, kill bool) error {
	signal := syscall.SIGTERM
	if kill {
		signal = syscall.SIGKILL
	}
	return signalSession(sessionID, signal)
}
