package fssecure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenPrivateConfigDetectsReplacementBetweenInspectionAndOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
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
	link := filepath.Join(dir, "agent.yaml")
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
	path := filepath.Join(dir, "agent.yaml")
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
	if err == nil || !strings.Contains(err.Error(), "changed while it was opened") {
		t.Fatalf("same-file symlink swap error = %v", err)
	}
}
