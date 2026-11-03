//go:build unix

package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func holdLock(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return f
}

func namedHolder(context.Context) string { return "pid 42 (golangci-lint run ./...)" }

func TestGateLintLockIsFreeWhenNobodyHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "golangci-lint.lock")
	release, err := acquireLintLock(t.Context(), path, time.Second, namedHolder)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		t.Fatal("a second runner took the lock the gate holds")
	}
	release()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("the lock stayed held after release: %v", err)
	}
}

func TestGateLintLockWaitFailsNamingTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "golangci-lint.lock")
	holdLock(t, path)
	_, err := acquireLintLock(t.Context(), path, 10*time.Millisecond, namedHolder)
	if err == nil {
		t.Fatal("the gate took a lock another runner holds")
	}
	for _, want := range []string{"still held after 10ms", "pid 42", "nothing was learned"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestGateLintLockWaitsForTheHolderToFinish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "golangci-lint.lock")
	held := holdLock(t, path)
	got := make(chan error, 1)
	go func() {
		release, err := acquireLintLock(context.Background(), path, time.Minute, namedHolder)
		if err == nil {
			release()
		}
		got <- err
	}()
	// safety: golangci-lint removes its lock file on release; the gate must lock
	// the file the path names afterwards, not the removed one it opened first.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(held.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil {
		t.Fatalf("the gate did not take the lock once it was released: %v", err)
	}
}

func TestGateLintSkipsGolangciLintsOwnWaitWhileHoldingTheLock(t *testing.T) {
	got := lockedLintCommand("abc123")
	if !strings.Contains(got, "--allow-parallel-runners") || strings.Contains(got, "--allow-serial-runners") {
		t.Fatalf("the gate would wait on the lock it already holds: %s", got)
	}
}
