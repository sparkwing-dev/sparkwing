//go:build linux

package procgroup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func guardedSessionSupport() error { return nil }

func sessionIdentity(pid int) (int, string, error) {
	statBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, "", fmt.Errorf("%w: process %d", ErrProcessAbsent, pid)
		}
		return 0, "", err
	}
	line := string(statBytes)
	commandEnd := strings.LastIndexByte(line, ')')
	if commandEnd < 0 || commandEnd+2 >= len(line) {
		return 0, "", fmt.Errorf("malformed process stat for %d", pid)
	}
	fields := strings.Fields(line[commandEnd+2:])
	if len(fields) < 20 {
		return 0, "", fmt.Errorf("short process stat for %d", pid)
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("process %d start time: %w", pid, err)
	}
	sessionID, err := unix.Getsid(pid)
	if err != nil {
		return 0, "", err
	}
	return sessionID, strconv.FormatUint(start, 10), nil
}

func signalGuardSession(sessionID int, kill bool) error {
	signal := syscall.SIGTERM
	if kill {
		signal = syscall.SIGKILL
	}
	return signalSession(context.Background(), sessionID, signal)
}

func signalDiagnosticSession(sessionID int) error {
	return signalSession(context.Background(), sessionID, syscall.SIGQUIT)
}
