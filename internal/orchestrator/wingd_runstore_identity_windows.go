//go:build windows

package orchestrator

import (
	"errors"
	"os"
)

func heldRunStoreIdentity(path string) (os.FileInfo, error) {
	// bug: Windows named Stat defers file identity until comparison, after a replacement may have occurred.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	closeErr := file.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	return info, nil
}
