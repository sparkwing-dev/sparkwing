package repos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

func TestDescribeDeadlineKillsTheWholeProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild")
	bin, _ := fakeSDK(t, "sleep 300 & echo $! > "+pidFile+"\nexec sleep 300")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := Describe(ctx, pipelineentry.Binary(bin), t.TempDir()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(pidfd) }()
	// safety: a pidfd turns readable when its process exits, so this waits on
	// the exit itself; the bound only stops a surviving grandchild hanging the test.
	fds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	if n, err := unix.Poll(fds, 10_000); err != nil || n != 1 {
		t.Fatalf("grandchild %d outlived the describe deadline (poll=%d, %v)", pid, n, err)
	}
}
