//go:build unix

package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestSDKInstallerNeverAnswers(t *testing.T) {
	input, hold, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = hold.Close() })
	command := exec.Command("sh", "-c", "trap '' TERM; cat")
	command.Stdin = input
	err = runSDKInstaller(t.Context(), command, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "after 1 attempt") {
		t.Fatalf("hung install: %v", err)
	}
	if command.ProcessState == nil {
		t.Fatal("installer was not reaped")
	}
	if err := syscall.Kill(command.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("installer survived: %v", err)
	}
	processes, err := procgroup.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		if process.Session == command.Process.Pid && !process.Terminated() {
			t.Fatalf("installer descendant survived: %+v", process)
		}
	}
}

func TestSDKInstallerKillsNestedProcessGroups(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	input, hold, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = hold.Close() })
	command := exec.Command(bash, "-c", "set -m; trap '' TERM; cat <&3 & echo $!; wait")
	command.ExtraFiles = []*os.File{input}
	var output bytes.Buffer
	command.Stdout = &output
	err = runSDKInstaller(t.Context(), command, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung install: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(output.String()))
	if err != nil {
		t.Fatalf("child did not start: %q: %v", output.String(), err)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	processes, err := procgroup.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		if process.PID == child && !process.Terminated() {
			t.Fatalf("nested group survived: %+v", process)
		}
	}
	if command.ProcessState == nil {
		t.Fatal("installer was not reaped")
	}
}

func TestSDKInstallerNonzeroExitIsNotRetried(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "attempts")
	command := exec.Command("sh", "-c", `printf x >> "$1"; exit 23`, "sdk-install", marker)
	err := runSDKInstaller(t.Context(), command, time.Second)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 || !strings.Contains(err.Error(), "after 1 attempt") {
		t.Fatalf("failed install: %v", err)
	}
	if command.ProcessState == nil {
		t.Fatal("failed installer was not reaped")
	}
	if attempts, err := os.ReadFile(marker); err != nil || string(attempts) != "x" {
		t.Fatalf("installer attempts = %q: %v", attempts, err)
	}
}
