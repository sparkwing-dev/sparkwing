//go:build !windows

package fssecure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecurePrivateDirAppliesOwnerOnlyMode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := SecurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != dirMode {
		t.Fatalf("private directory mode = %04o, want %04o", got, dirMode)
	}
}
