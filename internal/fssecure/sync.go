package fssecure

import (
	"errors"
	"os"
	"runtime"
)

// SyncDir persists a newly installed key or epoch's directory entry on supported filesystems.
// Windows refuses directory flushes; the file itself must be synced before installation.
func SyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = errors.Join(f.Sync(), f.Close())
	if runtime.GOOS == "windows" {
		return nil
	}
	return err
}
