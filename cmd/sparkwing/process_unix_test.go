//go:build !windows

package main

import (
	"os/exec"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestProcessAliveRejectsZombie(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		processes, err := procgroup.List()
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range processes {
			if process.PID == cmd.Process.Pid && process.State != "" && process.State[0] == 'Z' {
				if processAlive(process.PID) {
					t.Fatalf("zombie pid %d reported alive", process.PID)
				}
				return
			}
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("pid %d did not enter zombie state", cmd.Process.Pid)
		}
	}
}
