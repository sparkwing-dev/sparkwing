//go:build !windows

package fssecure_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestSecurePrivateDirAppliesOwnerOnlyMode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != fssecure.DirMode {
		t.Fatalf("private directory mode = %04o, want %04o", got, fssecure.DirMode)
	}
}
