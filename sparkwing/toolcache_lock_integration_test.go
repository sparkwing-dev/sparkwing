//go:build unix

package sparkwing_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const contentionMessage = "parallel golangci-lint is running"

func TestLintWithoutSerialRunnersFailsWhileAnotherRunHoldsTheLock(t *testing.T) {
	dir, tmp := lintFixture(t)
	release := holdLintLock(t, tmp, 0)
	defer release()

	out, err := lintWithLockTmp(t, dir, tmp, "")

	if err == nil {
		t.Fatalf("lint succeeded while the lock was held:\n%s", out)
	}
	if !strings.Contains(out, contentionMessage) {
		t.Fatalf("lint failed for some other reason than contention:\n%s", out)
	}
	if strings.Contains(out, "unusedHelper") {
		t.Fatalf("lint reported findings, so it was not blocked on the lock:\n%s", out)
	}
}

func TestLintWithSerialRunnersWaitsForTheLockThenRuns(t *testing.T) {
	dir, tmp := lintFixture(t)
	const held = 3 * time.Second
	release := holdLintLock(t, tmp, held)
	defer release()

	start := time.Now()
	out, _ := lintWithLockTmp(t, dir, tmp, "--allow-serial-runners")
	waited := time.Since(start)

	if strings.Contains(out, contentionMessage) {
		t.Fatalf("--allow-serial-runners still reported contention:\n%s", out)
	}
	if !strings.Contains(out, "unusedHelper") {
		t.Fatalf("lint produced no finding, so it never ran, so the lock let it through untaken:\n%s", out)
	}
	if waited < held {
		t.Fatalf("lint returned after %s without waiting out the %s holder", waited, held)
	}
}

func lintFixture(t *testing.T) (dir, tmp string) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs golangci-lint against a held lock")
	}
	for _, bin := range []string{"golangci-lint", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	return seedLintWorktree(t, filepath.Join(t.TempDir(), "wt")), t.TempDir()
}

func holdLintLock(t *testing.T, tmp string, hold time.Duration) (release func()) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(tmp, "golangci-lint.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("take lock: %v", err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		})
	}
	if hold > 0 {
		time.AfterFunc(hold, release)
	}
	return release
}

func lintWithLockTmp(t *testing.T, dir, tmp, flags string) (string, error) {
	t.Helper()
	useWorkDir(t, dir)

	line := "golangci-lint run --no-config --path-mode abs " + flags + " ./..."
	res, err := sparkwing.Bash(context.Background(), line).
		Dir(dir).
		Env("GOLANGCI_LINT_CACHE", toolCacheDir(t, "golangci-lint")).
		Env("TMPDIR", tmp).
		Capture()
	out := res.Stdout + res.Stderr
	var execErr *sparkwing.ExecError
	if errors.As(err, &execErr) {
		out = execErr.Stdout + execErr.Stderr
	}
	return out, err
}
