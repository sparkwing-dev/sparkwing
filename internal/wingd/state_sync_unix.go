//go:build !windows

package wingd

import (
	"errors"
	"fmt"
	"os"
)

func syncStateDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("wingd: open state directory: %w", err)
	}
	if err := errors.Join(handle.Sync(), handle.Close()); err != nil {
		return fmt.Errorf("wingd: sync state directory: %w", err)
	}
	return nil
}
