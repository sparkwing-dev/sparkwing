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

type Change struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

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

func RepairTree(root string, expected os.FileInfo, dryRun bool) ([]Change, error) {
	return repairTree(root, expected, dryRun)
}

func AuditSupported() bool { return auditSupported }
