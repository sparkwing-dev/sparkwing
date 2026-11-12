//go:build windows

package procgroup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestNaturalExitDuringOwnerTermination(t *testing.T) {
	for _, code := range []int{1, 23} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			for trial := 0; trial < 8; trial++ {
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				job, err := NewJob("")
				if err != nil {
					t.Fatal(err)
				}
				input, send, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				ready, output, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(ctx, "cmd.exe", "/d", "/q", "/v:on", "/c", "echo ready&set /p status=&exit /b !status!")
				cmd.Stdin, cmd.Stdout = input, output
				reachedWindow := false
				result := runOwnedCommand(ctx, cmd, func() error {
					if err := job.Start(cmd); err != nil {
						return err
					}
					line, err := bufio.NewReader(ready).ReadString('\n')
					if err != nil {
						return err
					}
					if strings.TrimSpace(line) != "ready" {
						return fmt.Errorf("child readiness: %q", line)
					}
					cancel()
					return nil
				}, func(handle windows.Handle) error {
					// safety: release an ordinary failure after the live check, then confirm death before termination.
					if _, err := fmt.Fprintln(send, code); err != nil {
						return err
					}
					state, err := windows.WaitForSingleObject(handle, 10000)
					if err != nil {
						return err
					}
					if state != windows.WAIT_OBJECT_0 {
						return fmt.Errorf("natural exit wait: %d", state)
					}
					reachedWindow = true
					return job.TerminateWithCode(ownerTerminationCode)
				}, nil)
				cancel()
				_ = input.Close()
				_ = send.Close()
				_ = ready.Close()
				_ = output.Close()
				_ = job.Close()
				if !reachedWindow {
					t.Fatalf("natural exit did not reach the termination window: %v", result)
				}
				var exit *exec.ExitError
				if !errors.As(result, &exit) || exit.ExitCode() != code {
					t.Fatalf("natural exit %d was erased into cancellation: %v", code, result)
				}
			}
		})
	}
}
