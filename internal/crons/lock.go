package crons

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// safety: two ticks would each read the cursor before either moved it, so the second is refused, not queued.
func lockTick(path string) (func(), error) {
	if path == "" {
		return nil, errors.New("crons: Service.LockPath is required to tick")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("crons: create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("crons: open %s: %w", path, err)
	}
	held, err := flockExclusiveNonblock(f)
	if err != nil {
		closeLockFile(f)
		return nil, fmt.Errorf("crons: lock %s: %w", path, err)
	}
	if !held {
		closeLockFile(f)
		return nil, ErrTickRunning
	}
	return func() {
		if err := flockUnlock(f); err != nil {
			slog.Default().Warn("release the crons tick lock", "path", path, "error", err)
		}
		closeLockFile(f)
	}, nil
}

func closeLockFile(f *os.File) {
	if err := f.Close(); err != nil {
		slog.Default().Warn("close the crons tick lock", "path", f.Name(), "error", err)
	}
}
