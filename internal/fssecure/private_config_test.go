package fssecure

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestOpenPrivateConfigDetectsReplacementBetweenInspectionAndOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	other := filepath.Join(dir, "replacement.yaml")
	for _, candidate := range []string{path, other} {
		if err := os.WriteFile(candidate, []byte("token: private\n"), FileMode); err != nil {
			t.Fatal(err)
		}
		if err := SecurePrivateConfig(candidate); err != nil {
			t.Fatal(err)
		}
	}
	f, err := openPrivateConfig(path, func(string) (*os.File, error) { return os.Open(other) })
	if f != nil {
		_ = f.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "changed while it was opened") {
		t.Fatalf("replacement error = %v", err)
	}
}

func TestOpenPrivateConfigRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	link := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(target, []byte("token: private\n"), FileMode); err != nil {
		t.Fatal(err)
	}
	if err := SecurePrivateConfig(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := OpenPrivateConfig(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestOpenPrivateConfigDetectsSameFileSymlinkSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	realPath := filepath.Join(dir, "original.yaml")
	if err := os.WriteFile(path, []byte("token: private\n"), FileMode); err != nil {
		t.Fatal(err)
	}
	if err := SecurePrivateConfig(path); err != nil {
		t.Fatal(err)
	}
	f, err := openPrivateConfig(path, func(candidate string) (*os.File, error) {
		opened, openErr := os.Open(candidate)
		if openErr != nil {
			return nil, openErr
		}
		if renameErr := os.Rename(candidate, realPath); renameErr != nil {
			_ = opened.Close()
			return nil, renameErr
		}
		if symlinkErr := os.Symlink(realPath, candidate); symlinkErr != nil {
			_ = opened.Close()
			t.Skipf("symlink unavailable: %v", symlinkErr)
		}
		return opened, nil
	})
	if f != nil {
		_ = f.Close()
	}
	if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(32)) {
		// safety: the fixture's os.Open handle denies replacement before the symlink swap can occur.
		if _, statErr := os.Lstat(realPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("blocked replacement left a renamed target: %v", statErr)
		}
		if f != nil {
			t.Fatal("failed replacement returned a readable file")
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "changed while it was opened") {
		t.Fatalf("same-file symlink swap error = %v", err)
	}
}
