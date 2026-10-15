package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func consumerCleanupError(action string, err error) {
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, action+":", err)
	}
}

func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func stopSupervisor(pid int, pidPath string) error {
	if err := signalTerminate(pid); err != nil {
		return fmt.Errorf("terminate pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			consumerCleanupError("remove consumer PID", os.Remove(pidPath))
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	consumerCleanupError("stop consumer", signalKill(pid))
	consumerCleanupError("remove consumer PID", os.Remove(pidPath))
	return nil
}
