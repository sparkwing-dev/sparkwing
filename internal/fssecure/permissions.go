// Package fssecure centralizes private on-disk modes for Sparkwing state.
package fssecure

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

const (
	DirMode  fs.FileMode = 0o700
	FileMode fs.FileMode = 0o600
)

// Change describes one path whose permissions need tightening.
type Change struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// EnsureDir creates path and removes group and other access from an existing
// directory on platforms with POSIX permissions. Existing owner restrictions
// remain because hardening a read-only directory must not make it writable.
func EnsureDir(path string) error {
	target := DirMode
	if info, err := os.Stat(path); err == nil {
		target = info.Mode().Perm() & DirMode
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, DirMode); err != nil {
		return err
	}
	return tighten(path, target)
}

// OpenFile opens a private file and tightens an existing file before return.
func OpenFile(path string, flag int) (*os.File, error) {
	f, err := os.OpenFile(path, flag, FileMode)
	if err != nil {
		return nil, err
	}
	if err := tightenOpen(f, FileMode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// WriteFile replaces a private file and tightens an existing file.
func WriteFile(path string, data []byte) error {
	f, err := OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// SecureFile removes group and other access from an existing file.
func SecureFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := tightenOpen(f, FileMode); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// PrepareSQLite creates or tightens a closed database file and every existing
// rollback sidecar. SQLite derives new WAL and SHM modes from the database.
// Callers must not use it while a SQLite connection to path is open.
func PrepareSQLite(path string) error {
	f, err := OpenFile(path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("secure sqlite database %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("secure sqlite database %s: %w", path, err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := path + suffix
		if err := SecureFile(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secure sqlite sidecar %s: %w", sidecar, err)
		}
	}
	return nil
}

// RepairTree reports and optionally tightens a Sparkwing home. It removes
// group, other, and special access without granting new owner permissions.
// expected identifies the root recognized by the caller; a replacement root
// is refused. Symlinks and special files are never followed or changed.
func RepairTree(root string, expected os.FileInfo, dryRun bool) ([]Change, error) {
	return repairTree(root, expected, dryRun)
}

// AuditSupported reports whether this platform exposes meaningful POSIX mode
// bits for the permission audit. Windows access is governed by DACLs, which
// os.Chmod's portable mode cannot verify or repair.
func AuditSupported() bool { return auditSupported }
