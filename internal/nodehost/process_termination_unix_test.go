//go:build unix

package nodehost

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestSessionTerminationPreservesEarlierExit(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonzero exit", true: "external SIGTERM"}[external], func(t *testing.T) {
			command := exec.Command("sh", "-c", "exit 7")
			if external {
				command = exec.Command("sh", "-c", "read value")
			}
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			group, err := procgroup.StartSession(command)
			if err != nil {
				t.Fatal(err)
			}
			process := groupProcess{Group: group, cmd: command}
			t.Cleanup(func() { _ = process.Terminate(t.Context(), time.Millisecond) })
			if external {
				if err := command.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			<-process.LeaderExited()
			err = process.Terminate(t.Context(), time.Millisecond)
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("earlier exit lost: %v", err)
			}
			if external {
				if status, ok := exit.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGTERM {
					t.Fatalf("external termination: %v", err)
				}
			} else if exit.ExitCode() != 7 {
				t.Fatalf("earlier status: %v", err)
			}
		})
	}
}
