//go:build windows

package fssecure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsCreatesPrivateFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	if err := EnsureDir(root); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFile(filepath.Join(root, "state"), os.O_CREATE|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
