//go:build !windows

package store

import (
	"errors"
	"os"
)

func openOutputForSync(path string) (*os.File, error) { return os.Open(path) }

func renameOutputFile(old, new string) error { return os.Rename(old, new) }

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
