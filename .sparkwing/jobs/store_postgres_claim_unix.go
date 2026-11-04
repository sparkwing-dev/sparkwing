//go:build unix

package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const storePostgresOwnerLock = "owner.lock"

// safety: a cancelled step gets one second before SIGKILL, too little to delete gigabytes.
// The kernel drops this flock however the process dies, so the next run can tell an abandoned
// root from a live one; the rename publishes the lock file only once it is held.
func claimStorePostgresRoot(root string) (func(), error) {
	pending := filepath.Join(root, storePostgresOwnerLock+".new")
	f, err := os.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create the postgres root lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock the postgres root: %w", err)
	}
	if err := os.Rename(pending, filepath.Join(root, storePostgresOwnerLock)); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("publish the postgres root lock: %w", err)
	}
	return func() { _ = f.Close() }, nil
}

// safety: a root without the lock file is left alone, since its run may still be creating it.
func sweepAbandonedStorePostgresRoots(ctx context.Context, dir string) {
	roots, err := filepath.Glob(filepath.Join(dir, storePostgresRootPrefix+"*"))
	if err != nil {
		return
	}
	for _, root := range roots {
		f, err := os.OpenFile(filepath.Join(root, storePostgresOwnerLock), os.O_RDWR, 0)
		if err != nil {
			continue
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err != nil {
			_ = f.Close()
			if !errors.Is(err, unix.EWOULDBLOCK) {
				sparkwing.Warn(ctx, "store-postgres: could not check %s: %v", root, err)
			}
			continue
		}
		removeErr := removeStorePostgresRoot(root)
		_ = f.Close()
		if removeErr != nil {
			sparkwing.Warn(ctx, "store-postgres: remove the abandoned root %s: %v", root, removeErr)
			continue
		}
		sparkwing.Info(ctx, "store-postgres: removed %s, left by a run that was killed before its teardown", root)
	}
}
