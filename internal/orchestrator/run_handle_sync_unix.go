//go:build !windows

package orchestrator

import (
	"fmt"
	"os"
)

func syncRunHandleDirectory(dir string) error {
	if d, err := os.Open(dir); err == nil {
		defer func() { _ = d.Close() }()
		if err := d.Sync(); err != nil {
			return fmt.Errorf("sync run-handle directory: %w", err)
		}
	}
	return nil
}
