//go:build unix

package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the gate takes golangci-lint's own box-wide lock before linting, so
// the five-minute lint deadline measures linting and a neighbor's run spends
// the separate wait bound instead. golangci-lint locks this same file with
// flock(2), so serial runners elsewhere still queue behind the gate.
func runGolangciLint(ctx context.Context) error {
	release, err := acquireLintLock(ctx, lintLockPath(), gateLintLockWait, lintLockHolders)
	if err != nil {
		return err
	}
	defer release()
	return lintModules(ctx, lintLockWait, lockedLintCommand, true)
}

func lintLockPath() string { return filepath.Join(os.TempDir(), "golangci-lint.lock") }

const lintLockPoll = time.Second

// safety: golangci-lint removes the lock file when it releases it, so a lock
// taken on a descriptor whose path now names another file guards nothing; the
// loop takes the lock again on the file the path names.
func acquireLintLock(ctx context.Context, path string, wait time.Duration, holders func(context.Context) string) (func(), error) {
	deadline := time.Now().Add(wait)
	announced := false
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("golangci-lint: open the box-wide lock %s: %w", path, err)
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if sameFile(f, path) {
				return func() {
					if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
						sparkwing.Warn(ctx, "golangci-lint: release the box-wide lock: %v", err)
					}
					if err := f.Close(); err != nil {
						sparkwing.Warn(ctx, "golangci-lint: close the box-wide lock: %v", err)
					}
				}, nil
			}
			closeQuietly(ctx, f)
			continue
		}
		closeQuietly(ctx, f)
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("golangci-lint: lock %s: %w", path, err)
		}
		if !announced {
			sparkwing.Info(ctx, "golangci-lint: waiting up to %s for the box-wide lock %s, held by %s", wait, path, holders(ctx))
			announced = true
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("golangci-lint: could not run -- the box-wide lock %s was still held after %s, by %s; "+
				"wait for that run to finish and rerun, or stop it if it is stuck; nothing was learned about this tree",
				path, wait, holders(ctx))
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("golangci-lint: stopped waiting for the box-wide lock: %w", ctx.Err())
		case <-time.After(lintLockPoll):
		}
	}
}

func sameFile(f *os.File, path string) bool {
	held, err := f.Stat()
	if err != nil {
		return false
	}
	named, err := os.Stat(path)
	return err == nil && os.SameFile(held, named)
}

func closeQuietly(ctx context.Context, f *os.File) {
	if err := f.Close(); err != nil {
		sparkwing.Warn(ctx, "golangci-lint: close the box-wide lock: %v", err)
	}
}

// safety: flock records no owner, so the holders are the golangci-lint
// processes running now; one of them holds the lock or is queued for it.
func lintLockHolders(ctx context.Context) string {
	out, err := sparkwing.Exec(ctx, "ps", "-eo", "pid=,etime=,args=").String()
	if err != nil {
		return "an unknown process (ps failed: " + err.Error() + ")"
	}
	self := strconv.Itoa(os.Getpid())
	var found []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == self {
			continue
		}
		if filepath.Base(fields[2]) != "golangci-lint" {
			continue
		}
		args := strings.Join(fields[2:], " ")
		if len(args) > 160 {
			args = args[:160] + "..."
		}
		found = append(found, fmt.Sprintf("pid %s (running %s: %s)", fields[0], fields[1], args))
	}
	if len(found) == 0 {
		return "no golangci-lint process visible to this user"
	}
	return strings.Join(found, "; ")
}
